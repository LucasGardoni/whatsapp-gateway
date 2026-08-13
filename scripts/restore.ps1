<#
.SYNOPSIS
    Restore do PostgreSQL + pasta de midia a partir de um backup gerado
    por backup.ps1 (fase 12). Rode numa maquina limpa pra validar o
    aceite da fase: "restore em maquina limpa reproduz o sistema".

.EXAMPLE
    .\scripts\restore.ps1 -PastaBackup "C:\backups\20260813-020000" -DatabaseUrl "postgres://gateway:gateway@localhost:5432/whatsapp_gateway" -MidiaDir "C:\dados\midia"

ATENCAO: pg_restore --clean apaga os objetos existentes no banco de
destino antes de recriar -- so aponte para um banco vazio ou descartavel,
nunca para o banco de producao em uso.

Requer pg_restore e goose no PATH (goose para aplicar migrations que
tenham sido criadas depois do backup).
#>
param(
    [Parameter(Mandatory = $true)][string]$PastaBackup,
    [Parameter(Mandatory = $true)][string]$DatabaseUrl,
    [Parameter(Mandatory = $true)][string]$MidiaDir,
    [string]$MigrationsDir = "migrations"
)

$ErrorActionPreference = "Stop"

$dumpPath = Join-Path $PastaBackup "whatsapp_gateway.dump"
if (-not (Test-Path $dumpPath)) { throw "dump nao encontrado em $dumpPath" }

Write-Host "Restaurando banco (pg_restore --clean --if-exists)..."
& pg_restore --clean --if-exists --no-owner --dbname=$DatabaseUrl $dumpPath
if ($LASTEXITCODE -ne 0) { throw "pg_restore falhou com codigo $LASTEXITCODE" }

Write-Host "Aplicando migrations pendentes (goose)..."
& goose -dir $MigrationsDir postgres $DatabaseUrl up
if ($LASTEXITCODE -ne 0) { throw "goose up falhou com codigo $LASTEXITCODE" }

Write-Host "Restaurando pasta de midia..."
$midiaOrigem = Join-Path $PastaBackup "midia"
New-Item -ItemType Directory -Force -Path $MidiaDir | Out-Null
robocopy $midiaOrigem $MidiaDir /E /NFL /NDL /NJH /NJS
if ($LASTEXITCODE -ge 8) { throw "robocopy falhou com codigo $LASTEXITCODE" }

Write-Host "Restore concluido. Suba o gateway e confirme GET /health antes de liberar trafego real."
