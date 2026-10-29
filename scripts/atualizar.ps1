<#
.SYNOPSIS
    Atualiza o gateway de producao a partir de um pacote de releases\ (gerado
    pelo publicar.ps1): migrations, troca do binario, restart e health check.

.DESCRIPTION
    Roda NO SERVIDOR, como administrador. Ordem (a mesma do implantar.ps1,
    exigida desde a migration 00026): parar, migrar, trocar, subir.

      1. le binario, DATABASE_URL e PORT do registro do servico (onde o NSSM
         guarda). As variaveis NUNCA sao regravadas: a fonte da verdade e o
         NSSM, nao o gateway.env (desatualizado, e o binario nao le).
      2. confere tudo ANTES de parar: nada que possa faltar depois do stop.
      3. com migration pendente, faz pg_dump antes (se houver pg_dump no PATH);
      4. para o servico, copia as migrations do pacote e roda goose up;
      5. guarda o binario atual em anteriores\ e poe o novo no lugar;
      6. sobe e espera GET /health. Sem resposta: volta o binario anterior.

    Garantia: qualquer falha depois do stop sobe o servico de novo, com o
    binario que estava antes. As migrations ficam (sao sempre aditivas).

    Tudo vai para logs\deploy-<data>.log.

.EXAMPLE
    .\scripts\atualizar.ps1                       # pacote mais novo de releases\
    .\scripts\atualizar.ps1 -Pacote 20261029-1530-ab12cd3
    .\scripts\atualizar.ps1 -Voltar               # sobe o binario anterior
#>
param(
    [string]$Raiz = "F:\www\whatsapp-gateway",
    [string]$Servico = "WhatsappGateway",
    [string]$Pacote = "",
    [switch]$Voltar,
    [switch]$SemBackup,
    [int]$GuardarAnteriores = 8
)

$ErrorActionPreference = "Stop"
$inicio = Get-Date -Format "yyyyMMdd-HHmmss"
New-Item -ItemType Directory -Force -Path (Join-Path $Raiz "logs") | Out-Null
Start-Transcript -Path (Join-Path $Raiz "logs\deploy-$inicio.log") | Out-Null

$nssm = Join-Path $Raiz "nssm.exe"
$goose = Join-Path $Raiz "goose.exe"
$migrations = Join-Path $Raiz "migrations"
$releases = Join-Path $Raiz "releases"
$anteriores = Join-Path $Raiz "anteriores"

# goose e pg_dump escrevem no stderr. No PowerShell 5.1, stderr redirecionado
# vira erro e o Stop aborta; aqui vira texto, entra no log e o codigo de saida
# decide. Devolve as linhas para quem precisa ler (status do goose).
function Rodar([string]$exe, [string[]]$argumentos) {
    $ErrorActionPreference = "Continue"
    $linhas = @(& $exe @argumentos 2>&1 | ForEach-Object { "$_" })
    $codigo = $LASTEXITCODE
    $linhas | ForEach-Object { Write-Host "  $_" }
    if ($codigo -ne 0) { throw "$(Split-Path $exe -Leaf) terminou com codigo $codigo." }
    return $linhas
}

function Estado { (Get-Service -Name $Servico).Status }

function Parar {
    Write-Host "Parando $Servico..."
    # o nssm sai com erro (STOP_PENDING) quando o gateway demora a fechar; quem
    # decide e o estado do servico, logo abaixo
    $ErrorActionPreference = "Continue"
    & $nssm stop $Servico 2>&1 | ForEach-Object { Write-Host ("  " + ("$_" -replace "`0", "")) }
    $ErrorActionPreference = "Stop"
    # o gateway leva ate ~10s fechando o outbox; o exe fica travado ate sair
    for ($i = 0; $i -lt 40 -and (Estado) -ne 'Stopped'; $i++) { Start-Sleep -Seconds 1 }
    if ((Estado) -ne 'Stopped') { throw "$Servico nao parou em 40s (estado: $(Estado))." }
    Write-Host "  parado."
}

function Subir {
    Write-Host "Subindo $Servico..."
    $ErrorActionPreference = "Continue"
    & $nssm start $Servico 2>&1 | ForEach-Object { Write-Host ("  " + ("$_" -replace "`0", "")) }
}

function Esperar-Health([string]$porta) {
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Seconds 1
        try {
            $r = Invoke-WebRequest -Uri "http://localhost:$porta/health" -UseBasicParsing -TimeoutSec 3
            if ($r.StatusCode -eq 200) { return $true }
        } catch { }
    }
    return $false
}

$parado = $false
$trocado = $false
$guardado = ""
$exe = ""

try {
    # ------------------------------------------------------------ servico (registro)
    $chave = "HKLM:\SYSTEM\CurrentControlSet\Services\$Servico\Parameters"
    if (-not (Test-Path $chave)) { throw "Servico $Servico nao encontrado no registro ($chave)." }
    $parametros = Get-ItemProperty -Path $chave
    $exe = [Environment]::ExpandEnvironmentVariables([string]$parametros.Application)
    if (-not [IO.Path]::IsPathRooted($exe) -or -not (Test-Path $exe)) { throw "Binario do servico invalido: '$exe'." }

    $variaveis = @{}
    foreach ($linha in @($parametros.AppEnvironmentExtra)) {
        $i = "$linha".IndexOf("=")
        if ($i -gt 0) { $variaveis["$linha".Substring(0, $i)] = "$linha".Substring($i + 1) }
    }
    $databaseUrl = $variaveis["DATABASE_URL"]
    $porta = if ($variaveis["PORT"]) { $variaveis["PORT"] } else { "8080" }
    if (-not $databaseUrl) { throw "DATABASE_URL nao esta nas variaveis do servico $Servico." }

    foreach ($obrigatorio in $nssm, $goose) {
        if (-not (Test-Path $obrigatorio)) { throw "Nao encontrado: $obrigatorio" }
    }
    New-Item -ItemType Directory -Force -Path $anteriores | Out-Null
    Write-Host "Servico: $Servico ($(Estado)) | binario: $exe | porta: $porta"

    # ------------------------------------------------------------ voltar
    if ($Voltar) {
        $ultimo = Get-ChildItem $anteriores -Filter *.exe | Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if (-not $ultimo) { throw "Nada em $anteriores para voltar." }
        $guardado = Join-Path $anteriores "revertido-$inicio.exe"
        Copy-Item $exe $guardado
        Parar; $parado = $true
        Copy-Item $ultimo.FullName $exe -Force; $trocado = $true
        Subir
        if (-not (Esperar-Health $porta)) { throw "Voltou para $($ultimo.Name), mas /health nao respondeu." }
        $parado = $false
        Write-Host "No ar com $($ultimo.Name)." -ForegroundColor Green
        return
    }

    # ------------------------------------------------------------ pacote
    $dir = if ($Pacote) { Join-Path $releases $Pacote } else {
        (Get-ChildItem $releases -Directory | Sort-Object Name -Descending | Select-Object -First 1).FullName
    }
    $novo = Join-Path $dir "gateway.exe"
    if (-not $dir -or -not (Test-Path $novo)) { throw "Pacote sem gateway.exe: $dir" }
    Write-Host "Pacote: $dir"
    if (Test-Path (Join-Path $dir "versao.txt")) { Get-Content (Join-Path $dir "versao.txt") | ForEach-Object { Write-Host "  $_" } }

    # ------------------------------------------------------------ pendentes + backup
    Write-Host "goose status..."
    $status = Rodar $goose @("-dir", (Join-Path $dir "migrations"), "postgres", $databaseUrl, "status")
    $pendentes = @($status | Where-Object { $_ -match "Pending" })
    Write-Host "Migrations pendentes: $($pendentes.Count)"

    if ($pendentes.Count -gt 0 -and -not $SemBackup) {
        if (Get-Command pg_dump -ErrorAction SilentlyContinue) {
            $backups = Join-Path $Raiz "backups"
            New-Item -ItemType Directory -Force -Path $backups | Out-Null
            $dump = Join-Path $backups "pre-deploy-$inicio.dump"
            Write-Host "Backup antes de migrar: $dump"
            Rodar "pg_dump" @("--format=custom", "--file=$dump", $databaseUrl) | Out-Null
        } else {
            Write-Warning "pg_dump fora do PATH: migrando sem backup."
        }
    }

    # copia do binario atual ANTES de parar: se faltar disco ou permissao, falha com o servico no ar
    $guardado = Join-Path $anteriores "gateway-antes-de-$inicio.exe"
    Copy-Item $exe $guardado

    # ------------------------------------------------------------ parar + migrar + trocar
    Parar; $parado = $true

    Copy-Item (Join-Path $dir "migrations\*.sql") $migrations -Force
    if ($pendentes.Count -gt 0) {
        Write-Host "goose up..."
        Rodar $goose @("-dir", $migrations, "postgres", $databaseUrl, "up") | Out-Null
    }

    Copy-Item $novo $exe -Force; $trocado = $true
    Write-Host "Binario trocado."
    Subir

    if (-not (Esperar-Health $porta)) {
        Write-Warning "/health nao respondeu em 30s. Ultimas linhas do log:"
        Get-Content (Join-Path $Raiz "logs\gateway.log") -Tail 30 -ErrorAction SilentlyContinue | ForEach-Object { Write-Host "  $_" }
        throw "Binario novo nao respondeu ao /health."
    }
    $parado = $false
    Write-Host "Gateway no ar com o pacote $(Split-Path $dir -Leaf)." -ForegroundColor Green

    # ------------------------------------------------------------ limpeza
    Get-ChildItem $anteriores -Filter *.exe | Sort-Object LastWriteTime -Descending |
        Select-Object -Skip $GuardarAnteriores | Remove-Item -Force
    Get-ChildItem $releases -Directory | Sort-Object Name -Descending |
        Select-Object -Skip $GuardarAnteriores | Remove-Item -Recurse -Force
} catch {
    $erro = $_
    Write-Host ""
    Write-Host "ERRO: $($erro.Exception.Message)" -ForegroundColor Red

    # o servico nunca fica parado por causa do deploy
    if ($parado) {
        try {
            if ((Estado) -ne 'Stopped') { Parar }
            if ($trocado -and $guardado -and (Test-Path $guardado)) {
                Write-Warning "Voltando o binario anterior..."
                Copy-Item $guardado $exe -Force
            }
            Subir
            if (Esperar-Health $porta) {
                Write-Host "Servico de volta no ar com o binario anterior." -ForegroundColor Yellow
            } else {
                Write-Host "ATENCAO: o servico NAO respondeu nem com o binario anterior. Ver logs\gateway.log." -ForegroundColor Red
            }
        } catch {
            Write-Host "ATENCAO: falhou ao religar o servico: $($_.Exception.Message)" -ForegroundColor Red
            Write-Host "Suba a mao: cd $Raiz ; .\nssm.exe start $Servico" -ForegroundColor Red
        }
    }
    Stop-Transcript | Out-Null
    exit 1
}

Stop-Transcript | Out-Null
