# Sobe o gateway em desenvolvimento carregando o .env.
#
# O binario Go le variaveis de ambiente e nao le o .env sozinho (nao ha
# biblioteca de dotenv no go.mod, de proposito -- em producao as variaveis
# vem do servico Windows). Este script existe so pra dev nao ter que
# exportar 15 variaveis a mao a cada reinicio.
#
#   .\scripts\dev-executar.ps1              # build + executa
#   .\scripts\dev-executar.ps1 -SemBuild    # executa o gateway.exe atual

param(
    [switch]$SemBuild,
    [string]$Arquivo = ".env"
)

$ErrorActionPreference = "Stop"
$raiz = Split-Path -Parent $PSScriptRoot
Set-Location $raiz

if (-not (Test-Path $Arquivo)) {
    Write-Error "$Arquivo nao encontrado. Copie o .env.example e preencha."
}

# comentarios e linhas vazias fora; o primeiro '=' separa (valores podem
# conter '=', como base64 com padding -- o GATEWAY_SERVICE_TOKEN e um caso).
$carregadas = 0
foreach ($linha in Get-Content $Arquivo) {
    $t = $linha.Trim()
    if ($t -eq "" -or $t.StartsWith("#")) { continue }

    $i = $t.IndexOf("=")
    if ($i -lt 1) { continue }

    $chave  = $t.Substring(0, $i).Trim()
    $valor  = $t.Substring($i + 1).Trim()
    Set-Item -Path "env:$chave" -Value $valor
    $carregadas++
}
Write-Host "$carregadas variaveis carregadas de $Arquivo" -ForegroundColor DarkGray

# aviso que espelha o do main.go, mas antes de subir: sem o segredo no path,
# os webhooks respondem 404 e nada entra (P1-10).
if (-not $env:WEBHOOK_PATH_SECRET) {
    Write-Host "AVISO: WEBHOOK_PATH_SECRET vazio -- os webhooks vao responder 404" -ForegroundColor Yellow
}

Write-Host "PUBLIC_BASE_URL = $($env:PUBLIC_BASE_URL)" -ForegroundColor DarkGray

if (-not $SemBuild) {
    Write-Host "compilando..." -ForegroundColor DarkGray
    go build -o gateway.exe ./cmd/gateway
    if ($LASTEXITCODE -ne 0) { Write-Error "build falhou" }
}

& .\gateway.exe
