# Uses saved stage binaries so controls retain their actual implementations.
param([Parameter(Mandatory)][string]$Local, [switch]$ReportsOnly)
$ErrorActionPreference = 'Stop'
$repo=(Get-Location).Path
$goroot=(mise exec -- go env GOROOT).Trim()
$previous=$env:TRAIL_BENCH_BATCH_BYTES
function Normalize-Report {
    process {
        $text="$_"
        foreach($entry in @(@($repo,'<repo>'),@($goroot,'<cache>/toolchains/go'),@((Resolve-Path $Local).Path,'<profile-dir>'),@($env:TEMP,'<temp>'))) {
            if($entry[0]){$text=$text.Replace($entry[0],$entry[1]).Replace($entry[0].Replace('\','/'),$entry[1])}
        }
        $text.Replace('\','/')
    }
}
try {
    foreach($stage in @('reuse','batch49152')) {
        $env:TRAIL_BENCH_BATCH_BYTES=if($stage -eq 'reuse'){'0'}else{'49152'}
        foreach($operation in @('StartEnd','AddEvent','SetAttributes')) {
            $binary="$Local/$stage-$operation.test.exe"
            $profile="$Local/$stage-$operation-long-cpu.pprof"
            if (!$ReportsOnly) {
                & $binary '-test.run=^$' "-test.bench=^BenchmarkFileDrain/$operation`$" '-test.benchtime=1000000x' '-test.count=1' "-test.cpuprofile=$profile" 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$stage-$operation-long-cpu-run.txt"
                if($LASTEXITCODE -ne 0){throw 'Long CPU capture failed'}
            }
            mise exec -- go tool pprof -top -nodecount=80 $binary $profile 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$stage-$operation-long-cpu.txt"
            if($LASTEXITCODE -ne 0){throw 'Long CPU report failed'}
            mise exec -- go tool pprof -top -cum -nodecount=40 $binary $profile 2>&1 | Normalize-Report | Set-Content "benchmarks/file-drain/$stage-$operation-long-cpu-cumulative.txt"
            if($LASTEXITCODE -ne 0){throw 'Long cumulative CPU report failed'}
        }
    }
} finally {$env:TRAIL_BENCH_BATCH_BYTES=$previous}
