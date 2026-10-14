<#
.SYNOPSIS
  Поднимает локальную чат-модель (llama.cpp): OpenAI-совместимый
  /v1/chat/completions на 127.0.0.1:8080 — провайдер local в config.yaml.

.DESCRIPTION
  Тот же llama-server, что у базы знаний (scripts/rag-servers.ps1), но без
  --embedding: генерация текста. Стенду всё равно, кто отвечает на
  /chat/completions, — DeepSeek или эта модель, меняется только base_url.

  Где лежит llama.cpp и модели — параметр -Dir или LLAMA_CPP_DIR;
  по умолчанию $HOME\tools\llama. Модель по умолчанию — Qwen3-4B в Q4_K_M:
    https://huggingface.co/Qwen/Qwen3-4B-GGUF  (Qwen3-4B-Q4_K_M.gguf, 2.5 ГБ)
  Для сравнения квантований там же лежит Q8_0 (4.3 ГБ): -Model Qwen3-4B-Q8_0.gguf.

  --jinja включает шаблон чата из самой модели: без него не работают
  инструменты и переключатель рассуждений (chat_template_kwargs.enable_thinking).
  --alias — имя модели в API: по нему стенд находит цену и окно в config.yaml.

  Сервер уходит в фон, лог — <Dir>\chat.log. Повторный запуск ничего не
  делает, если порт уже отвечает; -Restart перезапускает с новыми параметрами.

.EXAMPLE
  .\scripts\local-llm.ps1
  .\scripts\local-llm.ps1 -Model Qwen3-4B-Q8_0.gguf -Alias qwen3-4b-q8 -Restart
  .\scripts\local-llm.ps1 -Stop
#>
[CmdletBinding()]
param(
  [string]$Dir = $(if ($env:LLAMA_CPP_DIR) { $env:LLAMA_CPP_DIR } else { Join-Path $HOME 'tools\llama' }),
  [int]$Port = 8080,
  [string]$Model = 'Qwen3-4B-Q4_K_M.gguf',
  [string]$Alias = 'qwen3-4b',
  # Окно контекста в токенах. Больше окно — больше памяти под KV-кэш.
  [int]$Context = 8192,
  # Сколько запросов сервер обрабатывает одновременно; окно делится между ними.
  [int]$Parallel = 1,
  # -ngl 99 — все слои на GPU; 0 — только процессор.
  [int]$GpuLayers = 99,
  # Адрес, на котором слушать. 127.0.0.1 — только эта машина.
  [string]$Listen = '127.0.0.1',
  [string[]]$Extra = @(),
  [switch]$Restart,
  [switch]$Stop
)
$ErrorActionPreference = 'Stop'

function Test-Health([int]$Port) {
  try { (Invoke-RestMethod -Uri "http://127.0.0.1:$Port/health" -TimeoutSec 2).status -eq 'ok' } catch { $false }
}

# Останавливаем только сервер на своём порту: эмбеддинги и реранкер
# базы знаний — тоже llama-server, их трогать нельзя.
function Stop-Port([int]$Port) {
  $conn = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue
  foreach ($c in $conn) { Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue }
  $deadline = (Get-Date).AddSeconds(10)
  while ((Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) -and (Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 200
  }
}

if ($Stop) { Stop-Port $Port; Write-Host "чат-модель на :$Port остановлена"; return }

$server = Join-Path $Dir 'bin\llama-server.exe'
if (-not (Test-Path $server)) { throw "нет $server — укажи -Dir или LLAMA_CPP_DIR" }
$path = Join-Path $Dir "models\$Model"
if (-not (Test-Path $path)) { throw "нет модели $path" }

if (Test-Health $Port) {
  if (-not $Restart) { Write-Host "чат-модель уже работает на :$Port"; return }
  Stop-Port $Port
}

$argv = @('-m', $path, '--alias', $Alias, '--host', $Listen, '--port', "$Port",
          '-ngl', "$GpuLayers", '-c', "$Context", '-np', "$Parallel", '--jinja', '--metrics') + $Extra
Start-Process -FilePath $server -ArgumentList $argv -WindowStyle Hidden `
  -RedirectStandardError (Join-Path $Dir 'chat.log') | Out-Null
$deadline = (Get-Date).AddSeconds(120)
while (-not (Test-Health $Port)) {
  if ((Get-Date) -gt $deadline) { throw "чат-модель не поднялась за две минуты — смотри $Dir\chat.log" }
  Start-Sleep -Milliseconds 500
}
Write-Host "чат-модель готова: http://${Listen}:$Port/v1 ($Model как $Alias, окно $Context)"
