# code-mod.ps1 — AI 代码修改工具封装
# 用法: . .agent-tools\mod.ps1   (导入 mod 函数)
#        mod web_gui.go context
#        mod config.go create go
#        或直接: node .agent-tools\code-mod.js <file> <action> <args>

function mod {
  param(
    [Parameter(Mandatory=$true)][string]$File,
    [Parameter(Mandatory=$true)][string]$Action,
    [Parameter(ValueFromRemainingArguments=$true)]$ArgsList
  )
  $tool = Join-Path $PSScriptRoot ".agent-tools\code-mod.js"
  if (!(Test-Path $tool)) {
    $tool = Join-Path (Get-Location) ".agent-tools\code-mod.js"
  }
  if (!(Test-Path $tool)) {
    Write-Error "找不到 code-mod.js"
    return
  }
  $allArgs = @($File, $Action) + @($ArgsList | ForEach-Object { $_ })
  & node $tool @allArgs 2>&1
}

# 自动导入（如果本脚本被 dot-source）
if ($MyInvocation.InvocationName -eq ".") {
  Write-Host "mod 函数已加载" -ForegroundColor Green
}
