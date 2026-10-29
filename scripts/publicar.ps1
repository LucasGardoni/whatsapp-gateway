<#
.SYNOPSIS
    Deploy do gateway em um comando, da maquina de desenvolvimento.

.DESCRIPTION
    1. go vet + go test (pula com -PularTestes);
    2. compila o gateway.exe (windows/amd64);
    3. monta o pacote releases\<data>-<commit> no servidor, pelo
       compartilhamento administrativo: gateway.exe, migrations\,
       atualizar.ps1 e versao.txt;
    4. roda o atualizar.ps1 la via WinRM (migrations, troca, restart,
       health check e rollback). Com -SoEnviar, ou se o WinRM falhar, mostra
       o comando para rodar no servidor.

.EXAMPLE
    .\scripts\publicar.ps1
    .\scripts\publicar.ps1 -SoEnviar
    .\scripts\publicar.ps1 -Sim -PularTestes
#>
param(
    [string]$Servidor = "WEBMRE01",
    [string]$Raiz = "F:\www\whatsapp-gateway",
    [switch]$SoEnviar,
    [switch]$PularTestes,
    [switch]$Sim
)

$ErrorActionPreference = "Stop"
$repo = Split-Path $PSScriptRoot -Parent
Set-Location $repo

$Host.UI.RawUI.WindowTitle = "Deploy do gateway"
$logs = Join-Path $repo "bin\deploy-logs"
New-Item -ItemType Directory -Force -Path $logs | Out-Null
Start-Transcript -Path (Join-Path $logs ("publicar-" + (Get-Date -Format "yyyyMMdd-HHmmss") + ".log")) | Out-Null

$totalEtapas = if ($SoEnviar) { 4 } else { 5 }
$etapa = 0
function Etapa([string]$texto) {
    $script:etapa++
    Write-Host ""
    Write-Host ("[{0}] ({1}/{2}) {3}" -f (Get-Date -Format "HH:mm:ss"), $script:etapa, $totalEtapas, $texto) -ForegroundColor Cyan
    Write-Host ("-" * 70) -ForegroundColor DarkGray
}

function Ok([string]$texto) { Write-Host "  OK  $texto" -ForegroundColor Green }

try {

# ---------------------------------------------------------------- versao
Etapa "Versao"
$commit = (git rev-parse --short HEAD).Trim()
$branch = (git rev-parse --abbrev-ref HEAD).Trim()
$sujo = [bool](git status --porcelain --untracked-files=no)
$id = (Get-Date -Format "yyyyMMdd-HHmm") + "-" + $commit + $(if ($sujo) { "-sujo" } else { "" })

Write-Host "Branch $branch, commit $commit" -ForegroundColor Cyan
if ($sujo) { Write-Warning "Ha alteracoes sem commit: o pacote vai com elas (marcado -sujo)." }

Etapa "Testes"

# ---------------------------------------------------------------- testes
if ($PularTestes) {
    Write-Warning "Testes pulados (-PularTestes)."
} else {
    Write-Host "go vet..."
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet falhou." }
    Ok "go vet"
    Write-Host "go test (pode levar 1 minuto)..."
    # sem 2>&1: no PowerShell 5.1 o stderr de exe vira erro e o Stop aborta o script
    go test ./... | Where-Object { "$_" -notmatch "no test files" } | ForEach-Object { Write-Host "  $_" }
    if ($LASTEXITCODE -ne 0) { throw "testes falharam." }
    Ok "testes"
}

# ---------------------------------------------------------------- build
Etapa "Build do pacote"
$local = Join-Path $repo "bin\releases\$id"
New-Item -ItemType Directory -Force -Path (Join-Path $local "migrations") | Out-Null
$env:GOOS = "windows"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -trimpath -o (Join-Path $local "gateway.exe") ./cmd/gateway
if ($LASTEXITCODE -ne 0) { throw "go build falhou." }
Copy-Item "migrations\*.sql" (Join-Path $local "migrations")
Copy-Item "scripts\atualizar.ps1" $local
@(
    "pacote:  $id"
    "branch:  $branch"
    "commit:  $(git log -1 --format='%H %s')"
    "gerado:  $(Get-Date -Format 'yyyy-MM-dd HH:mm') por $env:USERNAME"
) | Set-Content (Join-Path $local "versao.txt") -Encoding UTF8

# ---------------------------------------------------------------- confirmar
$ultimaMigration = (Get-ChildItem "migrations\*.sql" | Sort-Object Name | Select-Object -Last 1).Name
Ok "pacote $id (ultima migration: $ultimaMigration)"
if (-not $Sim) {
    Write-Host ""
    $resposta = Read-Host "Publicar em $Servidor ($Raiz)? [s/N]"
    if ($resposta -notmatch '^[sS]') { Write-Host "Cancelado. Pacote local em $local" -ForegroundColor Yellow; exit 2 }
}

# ---------------------------------------------------------------- enviar
Etapa "Envio para $Servidor"
$compartilhamento = "\\$Servidor\" + $Raiz.Replace(":", "$") + "\releases\$id"
Write-Host "Copiando para $compartilhamento..."
New-Item -ItemType Directory -Force -Path $compartilhamento | Out-Null
Copy-Item "$local\*" $compartilhamento -Recurse -Force
Ok "copiado"

$comando = "powershell -ExecutionPolicy Bypass -File `"$Raiz\releases\$id\atualizar.ps1`" -Raiz `"$Raiz`" -Pacote $id"

if ($SoEnviar) {
    Write-Host "Enviado. No servidor, como administrador:" -ForegroundColor Yellow
    Write-Host "  $comando"
    exit 0
}

# ---------------------------------------------------------------- atualizar
Etapa "Atualizacao no servidor (migrations, troca, restart, health)"
try {
    Invoke-Command -ComputerName $Servidor -ErrorAction Stop -ScriptBlock {
        param($arquivo, $raiz, $pacote)
        # stderr do processo filho volta como texto: como erro, o -ErrorAction Stop daqui
        # cortaria a sessao no meio da atualizacao
        & powershell -NoProfile -ExecutionPolicy Bypass -File $arquivo -Raiz $raiz -Pacote $pacote 2>&1 | ForEach-Object { "$_" }
        if ($LASTEXITCODE -ne 0) { throw "atualizar.ps1 terminou com codigo $LASTEXITCODE" }
    } -ArgumentList "$Raiz\releases\$id\atualizar.ps1", $Raiz, $id
} catch {
    Write-Host ""
    Write-Host "Falhou: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host "O pacote ja esta no servidor. Para rodar la, como administrador:" -ForegroundColor Yellow
    Write-Host "  $comando"
    Write-Host "Log de cada execucao: $Raiz\logs\deploy-*.log"
    exit 1
}

Write-Host ""
Write-Host "Deploy $id concluido." -ForegroundColor Green
} catch {
    Write-Host ""
    Write-Host "ERRO: $($_.Exception.Message)" -ForegroundColor Red
    exit 1
} finally {
    Stop-Transcript | Out-Null
}
