# Run from repository root, serially after checks/profiles, with other heavy work idle.
param([string]$Benchstat = "benchstat")
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
foreach ($sink in @('memory', 'file')) {
    $name = if ($sink -eq 'memory') { 'core' } else { 'file' }
    mise exec -- go test . -run '^$' -bench "^Benchmark(AsyncEnabled|Enabled)/$sink/(StartEnd|AddEvent|SetAttributes)`$" -benchmem -benchtime=1s -count=10 2>&1 | Tee-Object -FilePath "benchmarks/optimized-producer/$name.txt"
    if ($LASTEXITCODE -ne 0) { throw "Final benchmark failed: $sink" }
    & $Benchstat "benchmarks/baseline/$name.txt" "benchmarks/optimized-producer/$name.txt" | Set-Content "benchmarks/optimized-producer/$name-benchstat.txt"
    if ($LASTEXITCODE -ne 0) { throw "Comparison failed: $sink" }
}
