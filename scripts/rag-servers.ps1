<#
.SYNOPSIS
  Поднимает локальные модели базы знаний (llama.cpp): эмбеддинги и реранкер.

.DESCRIPTION
  llama-server с флагом --embedding отдаёт OpenAI-совместимый
  /v1/embeddings — тот же эндпоинт, что у облачных API и Ollama, поэтому
  стенду всё равно, кто на том конце. Адрес и имя модели — в config.yaml,
  раздел embedders.

  Где лежит llama.cpp и модели — параметр -Dir или переменная окружения
  LLAMA_CPP_DIR; по умолчанию $HOME\tools\llama. Ожидается:
    <Dir>\bin\llama-server.exe        сборка с CUDA (или cpu/vulkan)
    <Dir>\models\bge-m3-Q8_0.gguf      https://huggingface.co/gpustack/bge-m3-GGUF
    <Dir>\models\bge-reranker-v2-m3-Q8_0.gguf   https://huggingface.co/gpustack/bge-reranker-v2-m3-GGUF

  Реранкер (день 23) — второй llama-server с --reranking: /v1/rerank.

  Серверы уходят в фон и пишут логи в <Dir>\embed.log и <Dir>\rerank.log.
  Повторный запуск ничего не делает, если порт уже отвечает.

.EXAMPLE
  .\scripts\rag-servers.ps1
  .\scripts\rag-servers.ps1 -Stop
#>
[CmdletBinding()]
param(
  [string]$Dir = $(if ($env:LLAMA_CPP_DIR) { $env:LLAMA_CPP_DIR } else { Join-Path $HOME 'tools\llama' }),
  [int]$EmbedPort = 8081,
  [string]$EmbedModel = 'bge-m3-Q8_0.gguf',
  # Реранкер (день 23) — второй сервер, на своём порту.
  [int]$RerankPort = 8082,
  [string]$RerankModel = 'bge-reranker-v2-m3-Q8_0.gguf',
  # -ngl 99 — все слои на GPU; без видеокарты поставить 0, будет медленнее,
  # но для базы в сотни чанков терпимо.
  [int]$GpuLayers = 99,
  [switch]$Stop
)
$ErrorActionPreference = 'Stop'

function Test-Health([int]$Port) {
  try { (Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 2).status -eq 'ok' } catch { $false }
}

if ($Stop) {
  Get-Process llama-server -ErrorAction SilentlyContinue | Stop-Process -Force
  Write-Host "llama-server остановлен"
  return
}

$server = Join-Path $Dir 'bin\llama-server.exe'
if (-not (Test-Path $server)) { throw "нет $server — укажи -Dir или LLAMA_CPP_DIR" }

function Start-Llama([string]$Name, [int]$Port, [string]$Model, [string[]]$Extra) {
  if (Test-Health $Port) { Write-Host "$Name уже работает на :$Port"; return }
  $path = Join-Path $Dir "models\$Model"
  if (-not (Test-Path $path)) { throw "нет модели $path" }
  # Окно и батч 8192: одна пачка чанков уходит одним батчем. bge-m3 держит
  # до 8192 токенов на текст, чанки стенда — по 300–600.
  $argv = @('-m', $path, '--host', '127.0.0.1', '--port', "$Port", '-ngl', "$GpuLayers",
            '-c', '8192', '-b', '8192', '-ub', '8192') + $Extra
  Start-Process -FilePath $server -ArgumentList $argv -WindowStyle Hidden `
    -RedirectStandardError (Join-Path $Dir "$Name.log") | Out-Null
  $deadline = (Get-Date).AddSeconds(60)
  while (-not (Test-Health $Port)) {
    if ((Get-Date) -gt $deadline) { throw "$Name не поднялся за минуту — смотри $Dir\$Name.log" }
    Start-Sleep -Milliseconds 500
  }
  Write-Host "$Name готов: http://127.0.0.1:$Port/v1 ($Model)"
}

Start-Llama 'embed' $EmbedPort $EmbedModel @('--embedding')
Start-Llama 'rerank' $RerankPort $RerankModel @('--reranking')

