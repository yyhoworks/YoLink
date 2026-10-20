param([string]$NasHost = 'nas')
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$evidence = Join-Path $repo '.impeccable/review/phase1-linux'
[void](New-Item -ItemType Directory -Force -Path $evidence)
$archive = Join-Path $evidence 'source.tar'
$revision = (& git -C $repo rev-parse HEAD).Trim()
if (& git -C $repo status --porcelain --untracked-files=normal) { throw 'Commit the exact candidate before Linux verification' }
& git -C $repo archive --format=tar -o $archive HEAD
if ($LASTEXITCODE -ne 0) { throw 'Source archive failed' }
$sshArgs = @('-T','-o','BatchMode=yes','-o','ConnectTimeout=10','-l','root',$NasHost)
$remote = ((& ssh @sshArgs 'mktemp -d /volume5/docker/.yolink-phase1-tests.XXXXXXXX') -join '').Trim()
if ($LASTEXITCODE -ne 0 -or $remote -notmatch '^/volume5/docker/\.yolink-phase1-tests\.[A-Za-z0-9]{8}$') { throw 'Invalid isolated test path' }
try {
    & scp -O -q -o BatchMode=yes -o ConnectTimeout=10 $archive "root@${NasHost}:$remote/source.tar"
    if ($LASTEXITCODE -ne 0) { throw 'Test upload failed' }
    $sourceHash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    $remoteHash = ((& ssh @sshArgs "sha256sum '$remote/source.tar'") -split '\s+')[0]
    if ($remoteHash -cne $sourceHash) { throw 'Uploaded source checksum mismatch' }
    # No production data or output is mounted. A bounded, dedicated directory
    # holds source, compiler cache and evidence; compiler container is disposable.
    $script = 'apk add --no-cache gcc musl-dev; mkdir /work/src; tar -xf /work/source.tar -C /work/src; cd /work/src; export GOCACHE=/work/cache GOPATH=/work/gopath CGO_ENABLED=1 GOMAXPROCS=2; go version; gcc --version; go test -race -count=1 -timeout=15m ./...; go vet ./...; go test -run "^$" -bench "^BenchmarkCheckpointProgress$" -benchtime=20x -count=2'
    $command = "/usr/local/bin/docker run --rm --memory=2g --cpus=2 --pids-limit=512 --mount type=bind,src=$remote,dst=/work --entrypoint /bin/sh golang:1.27-alpine -ec '$script'"
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $output = @(& ssh @sshArgs $command 2>&1 | ForEach-Object { "$_" })
    $resultCode = $LASTEXITCODE
    $timer.Stop()
    $current = (& git -C $repo rev-parse HEAD).Trim()
    $stable = $current -ceq $revision -and -not (& git -C $repo status --porcelain --untracked-files=normal)
    $result = [ordered]@{created_utc=[DateTime]::UtcNow.ToString('o'); git_head=$revision; source_tar_sha256=$sourceHash; source_unchanged=$stable; exit_code=$resultCode; elapsed_seconds=$timer.Elapsed.TotalSeconds; production_mounts=$false; command=$command; output=$output}
    $record = Join-Path $evidence ('result-'+[DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfff')+'.json')
    $result | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $record -Encoding utf8
    $output | Write-Output
    Write-Output "Evidence: $record"
    if ($resultCode -ne 0 -or -not $stable) { throw 'Linux race/regression/benchmark verification failed or candidate changed' }
} finally {
    # Exact mktemp target verified above; never remove any production path.
    $cleanup = "python3 -c `"import pathlib,shutil; p=pathlib.Path('$remote'); assert p.parent==pathlib.Path('/volume5/docker') and p.name.startswith('.yolink-phase1-tests.'); shutil.rmtree(str(p))`""
    & ssh @sshArgs $cleanup
    if ($LASTEXITCODE -ne 0) { Write-Warning "Private test directory retained: $remote" }
}
