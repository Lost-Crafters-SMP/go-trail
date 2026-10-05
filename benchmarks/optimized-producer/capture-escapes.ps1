param([Parameter(Mandatory)][string]$Candidate, [Parameter(Mandatory)][string]$Local)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force $Local | Out-Null
mise exec -- go build '-gcflags=go.lostcrafters.com/trail=-m=2' . 2>&1 | Set-Content "$local/producer-$Candidate-escape-full.txt"
if ($LASTEXITCODE -ne 0) { throw 'Escape build failed' }
# Derive function ranges from current source, not the baseline's old line numbers.
$ranges = @{}
foreach ($file in @('provider', 'span', 'attributes', 'async_processor', 'async_provider', 'owned_record', 'options', 'context')) {
    $lines = @(Get-Content "$file.go")
    $active = $false
    $selected = [System.Collections.Generic.HashSet[int]]::new()
    for ($i = 0; $i -lt $lines.Length; $i++) {
        if ($lines[$i] -match '^func ') {
            $active = $lines[$i] -match '(\bstart\(|\bend\(|\bAddEvent\(|\bSetAttributes\(|\baddEvent\(|\bsetAttributes\(|\bProcess\(|\bprocess\(|cloneAsync|resolve.*Attributes|resolveStartOptions|processResolved|processOwned|processFresh|ownFresh|admitLocked|ContextWithSpan)'
        }
        if ($active) { [void]$selected.Add($i + 1) }
    }
    $ranges[$file] = $selected
}
Get-Content "$local/producer-$Candidate-escape-full.txt" | Where-Object {
    if ($_ -notmatch '^\.[\\/](?<file>provider|span|attributes|async_processor|async_provider|owned_record|options|context)\.go:(?<line>\d+):') { return $false }
    $ranges[$Matches.file].Contains([int]$Matches.line) -and ($_ -match 'escapes to heap|does not escape|leaking param|leaks to|cannot inline|can inline')
} | Set-Content "benchmarks/optimized-producer/$Candidate-escape.txt"
