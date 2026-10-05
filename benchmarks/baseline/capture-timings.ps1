# Run from the repository root, with no concurrent checks/profiling/benchmarks.
$ErrorActionPreference = 'Stop'
foreach ($sink in @('memory', 'file')) {
    $output = if ($sink -eq 'memory') { 'core' } else { 'file' }
    mise exec -- go test . -run '^$' -bench "^Benchmark(AsyncEnabled|Enabled)/$sink/(StartEnd|AddEvent|SetAttributes)`$" -benchmem -benchtime=1s -count=10 2>&1 | Tee-Object -FilePath "benchmarks/baseline/$output.txt"
    if ($LASTEXITCODE -ne 0) { throw "Timing failed: $sink" }
}
