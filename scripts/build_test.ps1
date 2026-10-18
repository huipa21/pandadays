$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'build.ps1')

function Assert {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Assert-Fails {
    param([scriptblock]$Action, [string]$Pattern)
    try { & $Action } catch {
        Assert ($_.Exception.Message -match $Pattern) "Unexpected error: $_"
        return
    }
    throw "Expected failure matching: $Pattern"
}

$root = Join-Path ([IO.Path]::GetTempPath()) "pandadays-build-test-$([guid]::NewGuid())"
$oldDirty = $env:ALLOW_DIRTY
$oldAuthorDate = $env:GIT_AUTHOR_DATE
$oldCommitterDate = $env:GIT_COMMITTER_DATE
$script:calls = [Collections.Generic.List[object]]::new()
$script:failCommand = ''

function docker {
    $script:calls.Add(@($args))
    $global:LASTEXITCODE = if ($args[0] -eq $script:failCommand) { 23 } else { 0 }
}

function podman {
    docker @args
}

try {
    New-Item -ItemType Directory $root | Out-Null
    $env:ALLOW_DIRTY = $null
    Assert-Fails { Get-BuildIdentity $root } 'failed'
    Invoke-BuildCommand git @('-C', $root, 'init', '--quiet')
    Assert-Fails { Get-BuildIdentity $root } 'failed'
    Invoke-BuildCommand git @('-C', $root, 'config', 'user.name', 'Build Test')
    Invoke-BuildCommand git @('-C', $root, 'config', 'user.email', 'build@example.invalid')
    Set-Content (Join-Path $root '.gitignore') "/dist/`n"
    Set-Content (Join-Path $root 'source.txt') 'source'
    Invoke-BuildCommand git @('-C', $root, 'add', '.')
    $env:GIT_AUTHOR_DATE = '2026-10-10T13:31:30+08:00'
    $env:GIT_COMMITTER_DATE = $env:GIT_AUTHOR_DATE
    Invoke-BuildCommand git @('-C', $root, 'commit', '--quiet', '-m', 'fixture')
    $sha = (Invoke-BuildCommand git @('-C', $root, 'rev-parse', 'HEAD')).Trim()
    $expected = "20261010T053130Z-g$($sha.Substring(0, 12))"
    Assert ((Get-BuildIdentity $root) -ceq $expected) 'Version must use UTC commit date and fixed 12-character SHA'
    Assert ((Get-BuildIdentity $root) -ceq $expected) 'Version must be deterministic'

    Invoke-ImageBuild $root docker 'linux/amd64'
    Assert ($script:calls.Count -eq 2) 'Expected build then save'
    $build = $script:calls[0] -join ' '
    $save = $script:calls[1] -join ' '
    Assert ($build -like "buildx build --load --platform linux/amd64 --build-arg VERSION=$expected --tag localhost/pandadays:$expected --tag localhost/pandadays:latest *") 'Docker version and tags not wired'
    Assert ($save -like "save --output *pandadays-image.tar localhost/pandadays:$expected localhost/pandadays:latest") 'Archive must include both tags'
    Assert ((Get-BuildIdentity $root) -ceq $expected) 'Ignored output must not dirty the tree'

    $script:calls.Clear()
    Invoke-ImageBuild $root podman 'linux/arm64'
    Assert (($script:calls[0] -join ' ') -like 'build --format docker --platform linux/arm64 *') 'Podman must preserve Docker health checks and target platform'

    Set-Content (Join-Path $root 'source.txt') 'modified'
    Assert-Fails { Get-BuildIdentity $root } 'uncommitted changes'
    $env:ALLOW_DIRTY = '1'
    Assert ((Get-BuildIdentity $root) -ceq "$expected.dirty") 'Dirty override must mark version'
    $env:ALLOW_DIRTY = $null
    Invoke-BuildCommand git @('-C', $root, 'restore', 'source.txt')
    Set-Content (Join-Path $root 'untracked.txt') 'untracked'
    Assert-Fails { Get-BuildIdentity $root } 'uncommitted changes'
    Remove-Item (Join-Path $root 'untracked.txt')

    $script:calls.Clear()
    $script:failCommand = 'buildx'
    Assert-Fails { Invoke-ImageBuild $root docker 'linux/amd64' } 'exit 23'
    Assert ($script:calls.Count -eq 1) 'Failed build must not save an image'
    $script:failCommand = 'save'
    Assert-Fails { Invoke-ImageBuild $root docker 'linux/amd64' } 'exit 23'
    Write-Host 'PASS: commit identity, dirty checks, Docker/Podman tags, archive, and error propagation'
} finally {
    $env:ALLOW_DIRTY = $oldDirty
    $env:GIT_AUTHOR_DATE = $oldAuthorDate
    $env:GIT_COMMITTER_DATE = $oldCommitterDate
    Remove-Item -LiteralPath $root -Recurse -Force
}
