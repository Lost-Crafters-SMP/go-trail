param([Parameter(Mandatory)][string]$Stage, [Parameter(Mandatory)][string]$Local, [string]$Benchstat = 'benchstat', [switch]$ProfilesOnly, [int]$BatchBytes = 0)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
New-Item -ItemType Directory -Force $Local | Out-Null
$repo = (Get-Location).Path
$goroot = (mise exec -- go env GOROOT).Trim()
$previousBatchBytes = $env:TRAIL_BENCH_BATCH_BYTES
$env:TRAIL_BENCH_BATCH_BYTES = "$BatchBytes"
function Normalize-Report {
    process {
        $line = $_
        $text = "$line"
        foreach ($entry in @(@($repo, '<repo>'), @($goroot, '<cache>/toolchains/go'), @((Resolve-Path $Local).Path, '<profile-dir>'), @($env:TEMP, '<temp>'))) {
            if ($entry[0]) { $text = $text.Replace($entry[0], $entry[1]).Replace($entry[0].Replace('\', '/'), $entry[1]) }
        }
        $text.Replace('\', '/')
    }
}
$kinds = if ($BatchBytes -gt 0) { @('drain') } else { @('drain', 'producer') }
if (!$ProfilesOnly) { foreach ($kind in $kinds) {
    $bench = if ($kind -eq 'drain') { '^BenchmarkFileDrain/' } else { '^BenchmarkAsyncEnabled/file/(StartEnd|AddEvent|SetAttributes)$' }
    mise exec -- go test . -run '^$' -bench $bench -benchmem -benchtime=100000x -count=10 2>&1 | Tee-Object -FilePath "benchmarks/file-drain/$Stage-$kind.txt"
    if ($LASTEXITCODE -ne 0) { throw "Benchmark failed: $Stage/$kind" }
    if ($Stage -ne 'control') {
        & $Benchstat "benchmarks/file-drain/control-$kind.txt" "benchmarks/file-drain/$Stage-$kind.txt" | Set-Content "benchmarks/file-drain/$Stage-$kind-benchstat.txt"
        if ($LASTEXITCODE -ne 0) { throw 'benchstat failed' }
    }
    if ($BatchBytes -gt 0) {
        & $Benchstat "benchmarks/file-drain/reuse-$kind.txt" "benchmarks/file-drain/$Stage-$kind.txt" | Set-Content "benchmarks/file-drain/$Stage-vs-reuse-benchstat.txt"
    }
} }
foreach ($operation in @('StartEnd', 'AddEvent', 'SetAttributes')) {
    $name = "$Stage-$operation"
    $binary = "$Local/$name.test.exe"
    $memory = "$Local/$name.pprof"
    $cpu = "$Local/$name-cpu.pprof"
    mise exec -- go test . -run '^$' -bench "^BenchmarkFileDrain/$operation`$" -benchtime=100000x -count=1 -memprofilerate=1 "-memprofile=$memory" -o $binary 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$name-allocation-run.txt"
    if ($LASTEXITCODE -ne 0) { throw 'Allocation capture failed' }
    foreach ($sample in @('alloc_objects', 'alloc_space')) {
        mise exec -- go tool pprof -top "-$sample" -nodecount=30 -lines $binary $memory 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$name-$sample.txt"
    }
    mise exec -- go test . -run '^$' -bench "^BenchmarkFileDrain/$operation`$" -benchtime=100000x -count=1 "-cpuprofile=$cpu" -o $binary 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$name-cpu-run.txt"
    if ($LASTEXITCODE -ne 0) { throw 'CPU capture failed' }
    mise exec -- go tool pprof -top -nodecount=80 $binary $cpu 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$name-cpu.txt"
}
$env:TRAIL_BENCH_BATCH_BYTES = $previousBatchBytes
