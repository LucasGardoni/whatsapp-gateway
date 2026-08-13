<#
.SYNOPSIS
    Backup do PostgreSQL (dump custom format) e da pasta de midia (fase 12
    -- "Endurecimento". Secao 5 do plano registra isso como single point
    of failure conhecido: precisa estar rodando e testado antes do
    primeiro lead real).

.EXAMPLE
    .\scripts\backup.ps1 -DatabaseUrl "postgres://gateway:gateway@localhost:5432/whatsapp_gateway" -MidiaDir "C:\dados\midia" -DestinoDir "C:\backups"

Agendar via Windows Task Scheduler, no minimo diario. Requer pg_dump no
PATH (mesma major version do Postgres 16 usado em producao, secao 5).
#>
param(
    [Parameter(Mandatory = $true)][string]$DatabaseUrl,
    [Parameter(Mandatory = $true)][string]$MidiaDir,
    [Parameter(Mandatory = $true)][string]$DestinoDir,
    [int]$RetencaoDias = 14
)

$ErrorActionPreference = "Stop"

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$pastaBackup = Join-Path $DestinoDir $timestamp
New-Item -ItemType Directory -Force -Path $pastaBackup | Out-Null

Write-Host "Fazendo dump do PostgreSQL..."
$dumpPath = Join-Path $pastaBackup "whatsapp_gateway.dump"
& pg_dump --format=custom --file=$dumpPath $DatabaseUrl
if ($LASTEXITCODE -ne 0) { throw "pg_dump falhou com codigo $LASTEXITCODE" }

Write-Host "Copiando pasta de midia..."
$midiaDestino = Join-Path $pastaBackup "midia"
robocopy $MidiaDir $midiaDestino /E /NFL /NDL /NJH /NJS
if ($LASTEXITCODE -ge 8) { throw "robocopy falhou com codigo $LASTEXITCODE" }

Write-Host "Backup concluido em $pastaBackup"

Write-Host "Removendo backups com mais de $RetencaoDias dias..."
Get-ChildItem -Path $DestinoDir -Directory | Where-Object {
    $_.CreationTime -lt (Get-Date).AddDays(-$RetencaoDias)
} | Remove-Item -Recurse -Force

Write-Host "Rotina de backup finalizada."
