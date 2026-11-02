param(
    [int]$Port = 0
)

# Deterministic smoke test for scripts/fake-llm.cjs (EzTest 0.2 P0).
#
# Starts the fake upstream, asserts health/models/non-stream chat/SSE stream and
# determinism, then stops it. Exit code 0 only when every assertion passes.
#
# Usage:
#   pwsh -File scripts/fake-llm-smoke.ps1
#   pwsh -File scripts/fake-llm-smoke.ps1 -Port 13334
#
# Port resolution: -Port > FAKE_LLM_PORT > 13334. The fake only binds 127.0.0.1.

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = Split-Path -Parent $PSScriptRoot
$fakePath = Join-Path $PSScriptRoot "fake-llm.cjs"

if (-not (Test-Path -LiteralPath $fakePath)) {
    throw "fake-llm.cjs not found at $fakePath"
}
if ($Port -le 0) {
    $Port = if (-not [string]::IsNullOrWhiteSpace($env:FAKE_LLM_PORT)) { [int]$env:FAKE_LLM_PORT } else { 13334 }
}
if ($Port -lt 1 -or $Port -gt 65535) {
    throw "Port must be between 1 and 65535, got $Port"
}

$node = (Get-Command node -ErrorAction Stop).Source
$baseUrl = "http://127.0.0.1:$Port"
$outLog = Join-Path ([System.IO.Path]::GetTempPath()) ("eztest-fake-llm-" + [guid]::NewGuid().ToString("N") + ".out.log")
$errLog = Join-Path ([System.IO.Path]::GetTempPath()) ("eztest-fake-llm-" + [guid]::NewGuid().ToString("N") + ".err.log")
$proc = $null

function Test-PortOpen {
    param([string]$HostName, [int]$PortNumber)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect($HostName, $PortNumber, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne(300)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
}

function Read-LogTail {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { return "" }
    try {
        $text = Get-Content -LiteralPath $Path -Raw -ErrorAction Stop
        if ($null -eq $text) { return "" }
        if ($text.Length -gt 2000) { return $text.Substring($text.Length - 2000) }
        return $text
    } catch {
        return ""
    }
}

try {
    if (Test-PortOpen -HostName "127.0.0.1" -PortNumber $Port) {
        throw "Port $Port is already in use; stop the existing process or pass -Port <free-port>."
    }

    $env:FAKE_LLM_PORT = "$Port"
    $proc = Start-Process -FilePath $node `
        -ArgumentList @("`"$fakePath`"") `
        -WorkingDirectory $repoRoot `
        -PassThru `
        -WindowStyle Hidden `
        -RedirectStandardOutput $outLog `
        -RedirectStandardError $errLog

    # Wait for readiness.
    $health = $null
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        if ($proc.HasExited) {
            throw "fake LLM exited with code $($proc.ExitCode). stderr: $(Read-LogTail $errLog)"
        }
        try {
            $health = Invoke-RestMethod -Uri "$baseUrl/health" -Method Get -TimeoutSec 2
            break
        } catch {
            Start-Sleep -Milliseconds 200
        }
    }
    if ($null -eq $health) {
        throw "fake LLM did not become ready at $baseUrl/health within 15s. stderr: $(Read-LogTail $errLog)"
    }

    # 1) /health
    Assert-True ($health.status -eq "ok") "GET /health status should be 'ok', got '$($health.status)'"
    Assert-True ($health.service -eq "fake-llm") "GET /health service should be 'fake-llm'"
    Assert-True ([bool]$health.deterministic) "GET /health deterministic should be true"
    Assert-True ($health.models -contains "fake-llm") "GET /health models should contain 'fake-llm'"
    Write-Host "PASS GET /health: status=ok service=fake-llm"

    # 2) /v1/models
    $models = Invoke-RestMethod -Uri "$baseUrl/v1/models" -Method Get -TimeoutSec 5
    Assert-True ($models.object -eq "list") "GET /v1/models object should be 'list'"
    Assert-True (@($models.data).Count -ge 1) "GET /v1/models should return at least one model"
    $modelIds = @($models.data | ForEach-Object { $_.id })
    Assert-True ($modelIds -contains "fake-llm") "GET /v1/models should contain model 'fake-llm'"
    Write-Host ("PASS GET /v1/models: " + ($modelIds -join ", "))

    # 3) Non-stream chat completion.
    $chatBody = @{
        model    = "fake-llm"
        messages = @(
            @{ role = "user"; content = "smoke ping" }
        )
    } | ConvertTo-Json -Depth 10 -Compress

    $chatResponse = Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/v1/chat/completions" -Method Post `
        -ContentType "application/json" -Body $chatBody -TimeoutSec 15
    Assert-True ($chatResponse.StatusCode -eq 200) "POST /v1/chat/completions should return 200, got $($chatResponse.StatusCode)"
    $chat = $chatResponse.Content | ConvertFrom-Json
    Assert-True ($chat.object -eq "chat.completion") "chat object should be 'chat.completion'"
    Assert-True ($chat.model -eq "fake-llm") "chat model should echo 'fake-llm'"
    Assert-True (@($chat.choices).Count -eq 1) "chat should have exactly one choice"
    Assert-True ($chat.choices[0].message.role -eq "assistant") "chat role should be 'assistant'"
    $content = [string]$chat.choices[0].message.content
    Assert-True ($content -eq "EzTest fake LLM: smoke ping") "chat content mismatch: '$content'"
    Assert-True ($chat.choices[0].finish_reason -eq "stop") "chat finish_reason should be 'stop'"
    Assert-True ([int]$chat.usage.total_tokens -gt 0) "chat usage.total_tokens should be positive"
    Assert-True ([string]$chat.id -like "chatcmpl-fake-*") "chat id should start with 'chatcmpl-fake-'"
    Write-Host "PASS POST /v1/chat/completions (non-stream): deterministic text returned"

    # 4) Determinism: same request => same id/content.
    $chatAgainResponse = Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/v1/chat/completions" -Method Post `
        -ContentType "application/json" -Body $chatBody -TimeoutSec 15
    $chatAgain = $chatAgainResponse.Content | ConvertFrom-Json
    Assert-True ($chatAgain.id -eq $chat.id) "same request should return the same completion id"
    Assert-True ($chatAgain.choices[0].message.content -eq $content) "same request should return the same content"
    Write-Host "PASS determinism: repeated non-stream request is identical"

    # 5) Stream chat completion (stream=true SSE).
    $streamBody = @{
        model    = "fake-llm"
        stream   = $true
        messages = @(
            @{ role = "user"; content = "smoke ping" }
        )
    } | ConvertTo-Json -Depth 10 -Compress

    $streamResponse = Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/v1/chat/completions" -Method Post `
        -ContentType "application/json" -Headers @{ Accept = "text/event-stream" } -Body $streamBody -TimeoutSec 15
    Assert-True ($streamResponse.StatusCode -eq 200) "stream POST should return 200, got $($streamResponse.StatusCode)"
    $streamContentType = [string](@($streamResponse.Headers["Content-Type"])[0])
    Assert-True ($streamContentType -like "text/event-stream*") "stream Content-Type should be text/event-stream, got '$streamContentType'"
    $streamText = [string]$streamResponse.Content
    Assert-True ($streamText -match "data: \[DONE\]") "SSE stream should terminate with data: [DONE]"
    Assert-True ($streamText.Contains("chat.completion.chunk")) "SSE stream should contain chat.completion.chunk frames"

    $deltas = New-Object System.Collections.Generic.List[string]
    $chunkIds = New-Object System.Collections.Generic.HashSet[string]
    $lastFinishReason = $null
    foreach ($line in ($streamText -split "`n")) {
        $trimmed = $line.Trim()
        if (-not $trimmed.StartsWith("data: ")) { continue }
        $payload = $trimmed.Substring(6)
        if ($payload -eq "[DONE]") { continue }
        $chunk = $payload | ConvertFrom-Json
        [void]$chunkIds.Add([string]$chunk.id)
        $delta = $chunk.choices[0].delta
        $deltaNames = @($delta.PSObject.Properties | ForEach-Object { $_.Name })
        if ($deltaNames -contains "content" -and $null -ne $delta.content) {
            $deltas.Add([string]$delta.content)
        }
        if ($chunk.choices[0].finish_reason -ne $null) { $lastFinishReason = $chunk.choices[0].finish_reason }
    }
    $assembled = -join $deltas
    Assert-True ($assembled -eq $content) "streamed text should reassemble to the non-stream content; got '$assembled'"
    Assert-True ($lastFinishReason -eq "stop") "stream final finish_reason should be 'stop', got '$lastFinishReason'"
    Assert-True ($chunkIds.Count -eq 1) "all stream chunks should share one completion id, got $($chunkIds.Count)"
    $streamId = @($chunkIds)[0]
    Assert-True ($streamId -like "chatcmpl-fake-*") "stream completion id should start with 'chatcmpl-fake-', got '$streamId'"
    Write-Host "PASS POST /v1/chat/completions (stream=true): SSE chunks reassemble deterministically"

    Write-Host "SMOKE PASS: fake LLM on $baseUrl (health/models/chat/stream/determinism)"
}
finally {
    if ($null -ne $proc) {
        try {
            if (-not $proc.HasExited) {
                Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
                Wait-Process -Id $proc.Id -Timeout 5 -ErrorAction SilentlyContinue
            }
        } catch {
            # Best effort cleanup; the assertions above already reported failure.
        }
    }
    foreach ($log in @($outLog, $errLog)) {
        try {
            if (Test-Path -LiteralPath $log) { Remove-Item -LiteralPath $log -Force -ErrorAction SilentlyContinue }
        } catch {
            # Ignore temp cleanup failures.
        }
    }
}
