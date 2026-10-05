param([string]$Benchstat = 'benchstat')
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$previous = $env:TRAIL_BENCH_BATCH_BYTES
try {
    foreach ($size in @(0,3072,12288,49152)) {
        $stage = if ($size -eq 0) { 'reuse' } else { "batch$size" }
        $env:TRAIL_BENCH_BATCH_BYTES = "$size"
        mise exec -- go test . -run '^$' -bench '^BenchmarkFileDrainShutdown$' -benchmem -benchtime=256x -count=10 2>&1 | Tee-Object -FilePath "benchmarks/file-drain/$stage-shutdown.txt"
        if ($LASTEXITCODE -ne 0) { throw "Shutdown benchmark failed: $stage" }
        if ($size -gt 0) {
            & $Benchstat benchmarks/file-drain/reuse-shutdown.txt "benchmarks/file-drain/$stage-shutdown.txt" | Set-Content "benchmarks/file-drain/$stage-shutdown-benchstat.txt"
            if ($LASTEXITCODE -ne 0) { throw 'Shutdown comparison failed' }
        }
    }
} finally {
    $env:TRAIL_BENCH_BATCH_BYTES = $previous
}
