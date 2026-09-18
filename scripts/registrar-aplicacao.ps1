<#
.SYNOPSIS
    Registra uma aplicacao consumidora do barramento e imprime o token de
    servico dela (fase 1 -- docs/PLANO_BARRAMENTO_MENSAGENS.md, secao 5.1).

.DESCRIPTION
    Gera um token aleatorio de 256 bits, grava apenas o sha256 em
    aplicacao.token_hash e imprime o token em claro UMA vez. O gateway
    nunca guarda o token, so o hash -- se este valor for perdido, nao ha
    como recupera-lo: gere outro rodando o script de novo com -Rotacionar.

    O token em claro sai no console de proposito, e nao em arquivo: quem
    esta registrando a aplicacao ja tem o segredo na tela, e um arquivo
    temporario com token de servico e exatamente o tipo de coisa que fica
    esquecida no disco.

.EXAMPLE
    .\scripts\registrar-aplicacao.ps1 -Codigo portal -Nome "Portal Lider"

.EXAMPLE
    # troca o segredo de uma aplicacao ja registrada (o antigo para de valer)
    .\scripts\registrar-aplicacao.ps1 -Codigo portal -Nome "Portal Lider" -Rotacionar
#>
param(
    [Parameter(Mandatory = $true)][string]$Codigo,
    [Parameter(Mandatory = $true)][string]$Nome,
    [string]$DatabaseUrl = $env:DATABASE_URL,
    [switch]$Rotacionar
)

$ErrorActionPreference = "Stop"

if (-not $DatabaseUrl) {
    throw "Informe -DatabaseUrl ou defina DATABASE_URL."
}

# [A-Za-z0-9._~-] apenas: o token viaja em header Authorization e acaba
# colado em .env e em painel de terceiro. Base64 puro traria '+' e '/',
# que sobrevivem ao header mas quebram na primeira vez que alguem colar o
# valor numa URL de teste -- e o sintoma vira "401 sem motivo".
$bytes = [byte[]]::new(32)
[System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
$token = [Convert]::ToBase64String($bytes).Replace('+', '-').Replace('/', '_').TrimEnd('=')

$sha = [System.Security.Cryptography.SHA256]::Create()
$hash = ($sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($token)) | ForEach-Object { $_.ToString("x2") }) -join ''

# sem -Rotacionar o script e idempotente e NAO troca o segredo de uma
# aplicacao existente: rodar de novo por engano nao pode derrubar uma
# integracao que ja esta em producao.
$conflito = if ($Rotacionar) {
    "DO UPDATE SET token_hash = EXCLUDED.token_hash, nome = EXCLUDED.nome, ativo = true"
} else {
    "DO NOTHING"
}

$sql = @"
INSERT INTO aplicacao (codigo, nome, token_hash)
VALUES ('$Codigo', '$($Nome.Replace("'", "''"))', '$hash')
ON CONFLICT (codigo) $conflito
RETURNING id, codigo;
"@

$saida = $sql | & psql $DatabaseUrl --tuples-only --no-align 2>&1
if ($LASTEXITCODE -ne 0) { throw "psql falhou: $saida" }

if (-not $saida) {
    Write-Warning "Aplicacao '$Codigo' ja existe e o token NAO foi trocado. Use -Rotacionar para gerar um segredo novo."
    exit 0
}

Write-Host ""
Write-Host "Aplicacao registrada: $saida"
Write-Host ""
Write-Host "Token de servico (aparece uma unica vez -- guarde agora):"
Write-Host "  $token"
Write-Host ""
Write-Host "Uso: Authorization: Bearer $token"
Write-Host "Revogar depois:  UPDATE aplicacao SET ativo = false WHERE codigo = '$Codigo';"
Write-Host "  (o corte leva ate 30s por causa do cache do gateway)"
