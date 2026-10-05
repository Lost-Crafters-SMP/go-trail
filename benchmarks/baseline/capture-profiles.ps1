# Run from the repository root, after (not during) timing benchmarks.
# Full-rate allocation profiling is diagnostic, not a latency measurement.
param([Parameter(Mandatory)][string]$Local)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force $Local | Out-Null
$out = 'benchmarks/baseline'
foreach ($processor in @('sync', 'async')) {
    $benchmark = if ($processor -eq 'sync') { 'BenchmarkEnabled' } else { 'BenchmarkAsyncEnabled' }
    foreach ($operation in @('StartEnd', 'AddEvent', 'SetAttributes')) {
        $name = "$processor-$operation"
        $profile = "$Local/$name.pprof"
        $binary = "$Local/$name.test.exe"
        mise exec -- go test . -run '^$' -bench "^$benchmark/memory/$operation`$" -benchtime=100000x -count=1 -memprofilerate=1 "-memprofile=$profile" -o $binary 2>&1 | Set-Content "$out/$name-profile-run.txt"
        if ($LASTEXITCODE -ne 0) { throw "Profile failed: $name" }
        foreach ($sample in @('alloc_objects', 'alloc_space')) {
            mise exec -- go tool pprof -top "-$sample" -nodecount=30 -lines $binary $profile 2>&1 | Set-Content "$out/$name-$sample.txt"
            if ($LASTEXITCODE -ne 0) { throw "pprof top failed: $name/$sample" }
            mise exec -- go tool pprof "-$sample" '-list=cloneAsync|resolveSpanAttributes|resolveEventAttributes|providerState\)\.start|spanState\)\.(end|addEvent|setAttributes)|Span\.AddEvent' $binary $profile 2>&1 | Set-Content "$out/$name-$sample-lines.txt"
            if ($LASTEXITCODE -ne 0) { throw "pprof list failed: $name/$sample" }
        }
    }
}
foreach ($processor in @('sync', 'async')) {
    $benchmark = if ($processor -eq 'sync') { 'BenchmarkEnabled' } else { 'BenchmarkAsyncEnabled' }
    $name = "$processor-file-StartEnd"
    $profile = "$Local/$name.pprof"
    $cpu = "$Local/$name-cpu.pprof"
    $binary = "$Local/$name.test.exe"
    mise exec -- go test . -run '^$' -bench "^$benchmark/file/StartEnd`$" -benchtime=100000x -count=1 -memprofilerate=1 "-memprofile=$profile" -o $binary 2>&1 | Set-Content "$out/$name-profile-run.txt"
    if ($LASTEXITCODE -ne 0) { throw "Profile failed: $name" }
    foreach ($sample in @('alloc_objects', 'alloc_space')) {
        mise exec -- go tool pprof -top "-$sample" -nodecount=30 -lines $binary $profile 2>&1 | Set-Content "$out/$name-$sample.txt"
        if ($LASTEXITCODE -ne 0) { throw "pprof top failed: $name/$sample" }
        mise exec -- go tool pprof "-$sample" '-list=encodeRecord' $binary $profile 2>&1 | Set-Content "$out/$name-$sample-lines.txt"
        if ($LASTEXITCODE -ne 0) { throw "pprof list failed: $name/$sample" }
    }
    mise exec -- go test . -run '^$' -bench "^$benchmark/file/StartEnd`$" -benchtime=3s -count=1 "-cpuprofile=$cpu" -o $binary 2>&1 | Set-Content "$out/$name-cpu-run.txt"
    if ($LASTEXITCODE -ne 0) { throw "CPU profile failed: $name" }
    mise exec -- go tool pprof -top -nodecount=30 $binary $cpu 2>&1 | Set-Content "$out/$name-cpu.txt"
    if ($LASTEXITCODE -ne 0) { throw "pprof CPU top failed: $name" }
}
