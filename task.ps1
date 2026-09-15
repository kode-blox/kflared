[CmdletBinding(PositionalBinding = $false)]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $TaskArguments
)

$ErrorActionPreference = 'Stop'

# Run from the repository root so task paths and Taskfile.yaml are stable even
# when this wrapper is invoked from a subdirectory.
Set-Location -LiteralPath $PSScriptRoot

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Error 'Go is required to run the pinned Task runner.'
    exit 1
}

# The Task version is pinned by tools/task/go.mod. Using `go tool` keeps the
# runner local to this repository and works for every platform supported by Go.
$goArguments = @(
    '-C', (Join-Path $PSScriptRoot 'tools/task'),
    'tool', 'task',
    '--dir', '../..'
) + @($TaskArguments)

& go @goArguments
exit $LASTEXITCODE
