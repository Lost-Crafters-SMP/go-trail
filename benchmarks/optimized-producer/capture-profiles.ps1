# Full-rate allocation attribution only; run separately from timing capture.
param([Parameter(Mandatory)][string]$Local)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force $Local | Out-Null
foreach ($processor in @('sync', 'async')) {
    $benchmark = if ($processor -eq 'sync') { 'BenchmarkEnabled' } else { 'BenchmarkAsyncEnabled' }
    foreach ($operation in @('StartEnd', 'AddEvent', 'SetAttributes')) {
        $name = "final-$processor-$operation"
        $binary = "$Local/$name.test.exe"
        $profile = "$Local/$name.pprof"
        mise exec -- go test . -run '^$' -bench "^$benchmark/memory/$operation`$" -benchtime=100000x -count=1 -memprofilerate=1 "-memprofile=$profile" -o $binary 2>&1 | Set-Content "benchmarks/optimized-producer/$name-profile-run.txt"
        if ($LASTEXITCODE -ne 0) { throw "Profile failed: $name" }
        foreach ($sample in @('alloc_objects', 'alloc_space')) {
            mise exec -- go tool pprof -top "-$sample" -nodecount=30 -lines $binary $profile 2>&1 | Set-Content "benchmarks/optimized-producer/$name-$sample.txt"
            if ($LASTEXITCODE -ne 0) { throw "pprof failed: $name/$sample" }
        }
    }
}
