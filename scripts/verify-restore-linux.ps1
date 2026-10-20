param(
    [string]$Go = 'go',
    [string]$NasHost = 'nas',
    [switch]$Persistent,
    [string]$TestPattern = '^(TestBackup|TestDataLock|TestRestore)',
    [switch]$DiskFull
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$evidence = Join-Path $repo '.impeccable/review/backup-linux-20261021'
[void](New-Item -ItemType Directory -Force -Path $evidence)
$binary = Join-Path $evidence 'yolink-tests'
$sshArgs = @('-T','-o','BatchMode=yes','-o','ConnectTimeout=10','-l','root',$NasHost)
$sourceFiles = @(Get-ChildItem -LiteralPath $repo -Filter '*.go' -File) + @(Get-Item (Join-Path $repo 'go.mod'),(Join-Path $repo 'go.sum')) + @(Get-ChildItem -LiteralPath (Join-Path $repo 'web') -Recurse -File)
$before = @($sourceFiles | Sort-Object FullName | Get-FileHash -Algorithm SHA256 | Select-Object Path,Hash)
$priorGoos,$priorGoarch,$priorCgo = $env:GOOS,$env:GOARCH,$env:CGO_ENABLED
$remoteDir = $null
try {
    $env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
    Push-Location $repo
    try { & $Go test -c -o $binary .; if ($LASTEXITCODE -ne 0) { throw 'Linux test compilation failed' } } finally { Pop-Location }
    $remoteParent = if ($Persistent) { '/volume5/docker' } else { '/tmp' }
    $remotePrefix = if ($Persistent) { '.yolink-restore-drill.' } else { 'yolink-restore-drill.' }
    $created = & ssh @sshArgs "mktemp -d $remoteParent/${remotePrefix}XXXXXXXX"
    if ($LASTEXITCODE -ne 0) { throw 'Isolated remote directory creation failed' }
    $remoteDir = ($created -join '').Trim()
    $allowedRemote = '^'+[regex]::Escape("$remoteParent/$remotePrefix")+'[A-Za-z0-9]{8}$'
    if ($remoteDir -notmatch $allowedRemote) { $remoteDir = $null; throw 'Unexpected isolated remote path' }
    & scp -O -q -o BatchMode=yes -o ConnectTimeout=10 $binary "root@${NasHost}:$remoteDir/yolink-tests"
    if ($LASTEXITCODE -ne 0) { throw 'Isolated test upload failed' }
    & ssh @sshArgs "chmod 700 '$remoteDir/yolink-tests'"
    if ($LASTEXITCODE -ne 0) { throw 'Isolated test permission failed' }
    $diskMount = ''
    if ($DiskFull -and $Persistent) { throw 'Disk-full test must use only a bounded private tmpfs' }
    if ($Persistent) {
        & ssh @sshArgs "mkdir -m 700 '$remoteDir/scratch'"
        if ($LASTEXITCODE -ne 0) { throw 'Isolated persistent test directory creation failed' }
        $diskMount = "--mount type=bind,src=$remoteDir/scratch,dst=/scratch -e TMPDIR=/scratch"
    }
    if ($DiskFull) { $diskMount = '--tmpfs /scratch:rw,nosuid,nodev,size=64m -e TMPDIR=/scratch -e YOLINK_TEST_DISK_FULL=1' }
    if ($TestPattern.Contains("'")) { throw 'Unsupported test pattern quote' }
    $command = "/usr/local/bin/docker run --rm --network none --read-only --tmpfs /tmp:rw,nosuid,nodev,size=512m --memory 1g --cpu-shares 256 --mount type=bind,src=$remoteDir,dst=/drill,readonly $diskMount -e YOLINK_OS_NETWORK_ISOLATED=1 --entrypoint /drill/yolink-tests alpine:3.22 -test.v -test.timeout=30m -test.run '$TestPattern'"
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $output = @(& ssh @sshArgs $command 2>&1 | ForEach-Object { "$_" })
    $resultCode = $LASTEXITCODE
    $timer.Stop()
    $after = @($sourceFiles | Sort-Object FullName | Get-FileHash -Algorithm SHA256 | Select-Object Path,Hash)
    $stable = (ConvertTo-Json -InputObject $before -Compress) -ceq (ConvertTo-Json -InputObject $after -Compress)
    $result = [ordered]@{ created_utc=[DateTime]::UtcNow.ToString('o'); git_head=(& git -C $repo rev-parse HEAD); source_files=$before; binary_sha256=(Get-FileHash $binary).Hash; persistent_disk=[bool]$Persistent; command=$command; exit_code=$resultCode; elapsed_seconds=$timer.Elapsed.TotalSeconds; source_unchanged=$stable; output=$output }
    $result | ConvertTo-Json -Depth 7 | Set-Content -LiteralPath (Join-Path $evidence 'result.json') -Encoding utf8
    $result | ConvertTo-Json -Depth 7 | Set-Content -LiteralPath (Join-Path $evidence ('result-'+[DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfff')+'.json')) -Encoding utf8
    $output | Where-Object { $_ -match '^(--- PASS:|--- FAIL:|FAIL|PASS$|panic:|    [^ ]+\.go:)' } | Write-Output
    if (-not $stable) { throw 'Source changed during isolated drill; result cannot verify current snapshot' }
    if ($resultCode -ne 0) { throw "Isolated drill failed with exit code $resultCode" }
} finally {
    $env:GOOS=$priorGoos; $env:GOARCH=$priorGoarch; $env:CGO_ENABLED=$priorCgo
    if ($remoteDir -and $remoteDir -match $allowedRemote) {
        $cleanup = "python3 -c `"import pathlib,shutil; p=pathlib.Path('$remoteDir'); assert p.parent==pathlib.Path('$remoteParent') and p.name.startswith('$remotePrefix'); shutil.rmtree(str(p))`""
        & ssh @sshArgs $cleanup
        if ($LASTEXITCODE -ne 0) { Write-Warning "Isolated drill directory retained for cleanup: $remoteDir" }
    }
}
