# Run from repository root after timing benchmarks. Do not commit the full log.
param([Parameter(Mandatory)][string]$Local)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force $Local | Out-Null
mise exec -- go build '-gcflags=go.lostcrafters.com/trail=-m=2' . 2>&1 | Set-Content "$Local/escape-full.txt"
if ($LASTEXITCODE -ne 0) { throw 'Escape build failed' }
Get-Content "$Local/escape-full.txt" | Where-Object {
    if ($_ -notmatch '^\.[\\/](?<file>provider|span|attributes|async_processor|context)\.go:(?<line>\d+):') { return $false }
    $file = $Matches.file
    $line = [int]$Matches.line
    $hot = switch ($file) {
        'provider' { $line -ge 309 -and $line -le 446 }
        'span' { ($line -ge 40 -and $line -le 82) -or ($line -ge 116 -and $line -le 251) }
        'attributes' { $line -ge 225 -and $line -le 287 }
        'async_processor' { ($line -ge 132 -and $line -le 199) -or ($line -ge 466 -and $line -le 506) }
        'context' { $true }
    }
    $hot -and ($_ -match 'escapes to heap|does not escape|leaking param|leaks to|cannot inline|can inline')
} | Set-Content benchmarks/baseline/escape-hot-path.txt
