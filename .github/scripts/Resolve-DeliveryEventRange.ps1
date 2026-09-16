#Requires -Version 7.0

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue) {
  $PSNativeCommandUseErrorActionPreference = $false
}

function Invoke-Git {
  param(
    [Parameter(Mandatory)]
    [string[]] $Arguments
  )

  $output = @(& git @Arguments 2>$null)
  $exitCode = $LASTEXITCODE

  [pscustomobject]@{
    ExitCode = $exitCode
    Output = ($output -join "`n").Trim()
  }
}

function Stop-WithGitHubError {
  param(
    [Parameter(Mandatory)]
    [string] $Message
  )

  Write-Output "::error::$Message"
  exit 1
}

$baseRef = ''
$headRef = ''

switch ($env:EVENT_NAME) {
  'push' {
    break
  }
  'pull_request' {
    $baseRef = $env:PR_BASE_SHA
    $headRef = $env:PR_HEAD_SHA
    if ([string]::IsNullOrWhiteSpace($baseRef) -or [string]::IsNullOrWhiteSpace($headRef)) {
      Stop-WithGitHubError 'Pull request base or head commit SHA is unavailable.'
    }

    $baseResult = Invoke-Git -Arguments @('rev-parse', '--verify', "${baseRef}^{commit}")
    if ($baseResult.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($baseResult.Output)) {
      Stop-WithGitHubError "Unable to resolve pull request base commit ${baseRef}."
    }

    $headResult = Invoke-Git -Arguments @('rev-parse', '--verify', "${headRef}^{commit}")
    if ($headResult.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($headResult.Output)) {
      Stop-WithGitHubError "Unable to resolve pull request head commit ${headRef}."
    }

    $mergeBaseResult = Invoke-Git -Arguments @('merge-base', $baseResult.Output, $headResult.Output)
    if ($mergeBaseResult.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($mergeBaseResult.Output)) {
      Stop-WithGitHubError "Unable to resolve the merge base of pull request commits $($baseResult.Output) and $($headResult.Output)."
    }

    $baseRef = $mergeBaseResult.Output
    $headRef = $headResult.Output
    break
  }
  'workflow_dispatch' {
    $headRef = $env:SELECTED_SHA
    if ([string]::IsNullOrWhiteSpace($headRef)) {
      Stop-WithGitHubError 'The workflow_dispatch commit SHA is unavailable.'
    }

    $parentResult = Invoke-Git -Arguments @('rev-parse', '--verify', "${headRef}^1^{commit}")
    if ($parentResult.ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($parentResult.Output)) {
      Stop-WithGitHubError "Unable to resolve the first parent of workflow_dispatch commit ${headRef}."
    }

    $baseRef = $parentResult.Output
    break
  }
  default {
    Stop-WithGitHubError "Unsupported delivery-change event: $($env:EVENT_NAME)."
  }
}

"base-ref=$baseRef" >> $env:GITHUB_OUTPUT
"head-ref=$headRef" >> $env:GITHUB_OUTPUT
