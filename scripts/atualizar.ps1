<#
.SYNOPSIS
    Atualiza o gateway de producao a partir de um pacote de releases\ (gerado
    pelo publicar.ps1): migrations, troca do binario, restart e health check.

.DESCRIPTION
    Roda NO SERVIDOR, como administrador. Ordem (a mesma do implantar.ps1,
    exigida desde a migration 00026): parar, migrar, trocar, subir.

      1. le DATABASE_URL e PORT do proprio servico (nssm AppEnvironmentExtra).
         As variaveis NUNCA sao regravadas: a fonte da verdade e o NSSM, nao o
         gateway.env (que esta desatualizado e o binario nao le).
      2. com migration pendente, faz pg_dump antes (se houver pg_dump no PATH);
      3. para o servico, copia as migrations do pacote e roda goose up;
      4. guarda o binario atual em anteriores\ e poe o novo no lugar;
      5. sobe e espera GET /health. Sem resposta: volta o binario anterior e
         sobe de novo (as migrations ficam -- sao sempre aditivas).

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
$anteriores = Join-Path $Raiz "anteriores"
New-Item -ItemType Directory -Force -Path $anteriores | Out-Null

# saida do nssm vem em UTF-16: tira os nulos e o BOM
function Nssm-Get([string]$campo) {
    $bruto = & $nssm get $Servico $campo
    if ($LASTEXITCODE -ne 0) { throw "nssm get $campo falhou." }
    return @($bruto | ForEach-Object { ($_ -replace "`0", '') -replace '^[^\x20-\x7E]+', '' } | Where-Object { $_ -ne '' })
}

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

function Esperar-Health([string]$porta) {
    for ($i = 0; $i -lt 20; $i++) {
        Start-Sleep -Seconds 1
        try {
            $r = Invoke-WebRequest -Uri "http://localhost:$porta/health" -UseBasicParsing -TimeoutSec 3
            if ($r.StatusCode -eq 200) { return $true }
        } catch { }
    }
    return $false
}

function Parar {
    Write-Host "Parando $Servico..."
    & $nssm stop $Servico | Out-Null
    # o nssm devolve antes do processo sair de vez; o exe fica travado ate la
    for ($i = 0; $i -lt 30 -and (Get-Service $Servico).Status -ne 'Stopped'; $i++) { Start-Sleep -Seconds 1 }
}

function Subir {
    Write-Host "Subindo $Servico..."
    & $nssm start $Servico | Out-Null
}

try {
    $exe = (Nssm-Get "Application")[0]
    $variaveis = @{}
    foreach ($linha in (Nssm-Get "AppEnvironmentExtra")) {
        $i = $linha.IndexOf("=")
        if ($i -gt 0) { $variaveis[$linha.Substring(0, $i)] = $linha.Substring($i + 1) }
    }
    $databaseUrl = $variaveis["DATABASE_URL"]
    $porta = if ($variaveis["PORT"]) { $variaveis["PORT"] } else { "8080" }
    if (-not $databaseUrl) { throw "DATABASE_URL nao esta nas variaveis do servico $Servico." }
    Write-Host "Servico: $Servico | binario: $exe | porta: $porta"

    # ------------------------------------------------------------ voltar
    if ($Voltar) {
        $ultimo = Get-ChildItem $anteriores -Filter *.exe | Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if (-not $ultimo) { throw "Nada em $anteriores para voltar." }
        Parar
        Copy-Item $exe (Join-Path $anteriores ("revertido-$inicio.exe"))
        Copy-Item $ultimo.FullName $exe -Force
        Subir
        if (-not (Esperar-Health $porta)) { throw "Voltou para $($ultimo.Name), mas /health nao respondeu." }
        Write-Host "No ar com $($ultimo.Name)." -ForegroundColor Green
        return
    }

    # ------------------------------------------------------------ pacote
    $releases = Join-Path $Raiz "releases"
    $dir = if ($Pacote) { Join-Path $releases $Pacote } else {
        (Get-ChildItem $releases -Directory | Sort-Object Name -Descending | Select-Object -First 1).FullName
    }
    if (-not $dir -or -not (Test-Path (Join-Path $dir "gateway.exe"))) { throw "Pacote sem gateway.exe: $dir" }
    Write-Host "Pacote: $dir"
    if (Test-Path (Join-Path $dir "versao.txt")) { Get-Content (Join-Path $dir "versao.txt") | Write-Host }

    $goose = Join-Path $Raiz "goose.exe"
    $migrations = Join-Path $Raiz "migrations"

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

    # ------------------------------------------------------------ parar + migrar
    Parar
    try {
        Copy-Item (Join-Path $dir "migrations\*.sql") $migrations -Force
        if ($pendentes.Count -gt 0) {
            Write-Host "goose up..."
            Rodar $goose @("-dir", $migrations, "postgres", $databaseUrl, "up") | Out-Null
        }
    } catch {
        # binario antigo de volta no ar: migration que falhou roda em transacao
        Write-Warning "Falha ao migrar; subindo o binario que ja estava."
        Subir
        throw
    }

    # ------------------------------------------------------------ trocar
    $guardado = Join-Path $anteriores ("gateway-antes-de-$inicio.exe")
    Copy-Item $exe $guardado
    Copy-Item (Join-Path $dir "gateway.exe") $exe -Force
    Subir

    if (Esperar-Health $porta) {
        Write-Host "Gateway no ar com o pacote $(Split-Path $dir -Leaf)." -ForegroundColor Green
    } else {
        Write-Warning "/health nao respondeu. Ultimas linhas do log:"
        Get-Content (Join-Path $Raiz "logs\gateway.log") -Tail 30 -ErrorAction SilentlyContinue
        Write-Warning "Voltando o binario anterior..."
        Parar
        Copy-Item $guardado $exe -Force
        Subir
        $voltou = Esperar-Health $porta
        throw ("Deploy falhou; binario anterior " + $(if ($voltou) { "no ar." } else { "TAMBEM nao respondeu -- ver logs\gateway.log." }))
    }

    # ------------------------------------------------------------ limpeza
    Get-ChildItem $anteriores -Filter *.exe | Sort-Object LastWriteTime -Descending |
        Select-Object -Skip $GuardarAnteriores | Remove-Item -Force
    Get-ChildItem $releases -Directory | Sort-Object Name -Descending |
        Select-Object -Skip $GuardarAnteriores | Remove-Item -Recurse -Force
} finally {
    Stop-Transcript | Out-Null
}
