#!/usr/bin/env node
/**
 * code-mod.js — 给 AI 用的代码修改工具 (增强版)
 *
 * 用法:
 *   node .agent-tools/code-mod.js <file> <action> <args...>
 *
 * Actions:
 *   replace <old> <new>             替换字符串
 *   replacere <pattern> <new>       正则替换
 *   insert-before <target> <text>   在匹配行前插入
 *   insert-after <target> <text>    在匹配行后插入
 *   insert-line <lineno> <text>     在指定行号后插入
 *   delete <target>                 删除匹配行
 *   delete-range <from> <to>        删除行号范围
 *   delete-line <lineno>            删除指定行号
 *   show                            显示文件
 *   context                         显示带行号的文件
 *   grep <pattern>                  搜索匹配行 (带行号)
 *   func <name>                     显示函数/结构体定义
 *   append <text>                   在文件末尾追加
 *   create <template>               创建新文件 (go/empty)
 *
 * text 参数支持:
 *   @file:<path>    从另一个文件读取内容
 *
 * 示例:
 *   node .agent-tools/code-mod.js main.go replace "foo" "bar"
 *   node .agent-tools/code-mod.js main.go func handleUpdateConfig
 *   node .agent-tools/code-mod.js main.go grep "^func.*ECHPool"
 *   node .agent-tools/code-mod.js main.go create go
 *   node .agent-tools/code-mod.js config.go append @file:config_template.txt
 */

const fs = require("fs");
const path = require("path");

const [file, action, ...args] = process.argv.slice(2);

if (!file || !action) {
  console.error("用法: node code-mod.js <file> <action> [args...]\n");
  const help = {
    "replace <old> <new>": "替换字符串",
    "replacere <pattern> <new>": "正则替换",
    "insert-before <target> <text>": "在匹配行前插入",
    "insert-after <target> <text>": "在匹配行后插入",
    "insert-line <lineno> <text>": "在指定行号后插入",
    "delete <target>": "删除匹配行",
    "delete-range <from> <to>": "删除行号范围",
    "delete-line <lineno>": "删除指定行",
    "show": "显示文件",
    "context": "显示带行号的文件",
    "grep <pattern>": "搜索匹配行并显示行号",
    "func <name>": "显示函数/结构体定义范围",
    "append <text>": "在文件末尾追加",
    "create empty|go": "创建新文件"
  };
  for (const [k, v] of Object.entries(help)) {
    console.error("  " + k.padEnd(40) + v);
  }
  process.exit(1);
}

function resolveArg(arg) {
  if (!arg) return "";
  if (arg.startsWith("@file:")) {
    const p = arg.slice(6);
    return fs.readFileSync(path.resolve(p), "utf-8");
  }
  return arg;
}

function read(filePath) {
  return fs.readFileSync(path.resolve(filePath), "utf-8").replace(/^\uFEFF/, "");
}

function write(filePath, content) {
  fs.writeFileSync(path.resolve(filePath), content, "utf-8");
  console.error("✓ " + path.resolve(filePath) + " 已更新");
}

function linesWithIndices(content) {
  return content.split("\n").map((line, i) => ({ line, i }));
}

// ====== CREATE ======
if (action === "create") {
  const [template] = args;
  const tpl = template || "empty";
  const fullPath = path.resolve(file);
  if (fs.existsSync(fullPath)) {
    console.error("✗ 文件已存在: " + fullPath);
    process.exit(1);
  }
  let content = "";
  if (tpl === "go") {
    const baseName = path.basename(file, ".go");
    content = 'package main\n\nimport (\n\t"log"\n)\n\nfunc init() {\n\tlog.Println("' + baseName + ' initialized")\n}\n';
  } else {
    content = "";
  }
  fs.writeFileSync(fullPath, content, "utf-8");
  console.error("✓ 已创建: " + fullPath);
  process.exit(0);
}

// ====== READ-ONLY ACTIONS ======
if (action === "show") { process.stdout.write(read(file)); process.exit(0); }

let content = read(file);
const lines = content.split("\n");

function printContext(linesArray) {
  const pad = String(linesArray.length).length;
  linesArray.forEach((line, i) => {
    process.stdout.write(String(i + 1).padStart(pad) + " | " + line + "\n");
  });
}

if (action === "context") { printContext(lines); process.exit(0); }

if (action === "grep") {
  const [pattern] = args;
  if (!pattern) { console.error("✗ 缺少 <pattern>"); process.exit(1); }
  const re = new RegExp(pattern);
  printContext(lines.filter(l => re.test(l.line)));
  process.exit(0);
}

if (action === "func") {
  const [name] = args;
  if (!name) { console.error("✗ 缺少 <name>"); process.exit(1); }
  const inFunc = { depth: 0, active: false, startLine: 0, output: [] };
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    const funcMatch = l.match(/^(func\s+\w+|type\s+\w+\s+(struct|interface))\s*/);
    if (funcMatch && (l.includes(name) || name === "")) {
      if (inFunc.active) {
        // end previous
        break;
      }
      inFunc.active = true;
      inFunc.startLine = i;
      inFunc.depth = (l.match(/{/g) || []).length - (l.match(/}/g) || []).length;
      inFunc.output.push({ line: l, num: i });
    } else if (inFunc.active) {
      inFunc.depth += (l.match(/{/g) || []).length;
      inFunc.depth -= (l.match(/}/g) || []).length;
      inFunc.output.push({ line: l, num: i });
      if (inFunc.depth <= 0) {
        break;
      }
    }
  }
  if (inFunc.output.length === 0) {
    console.error("✗ 未找到: " + name);
    process.exit(1);
  }
  const pad = String(inFunc.output[inFunc.output.length-1].num + 1).length;
  for (const o of inFunc.output) {
    process.stdout.write(String(o.num + 1).padStart(pad) + " | " + o.line + "\n");
  }
  process.exit(0);
}

// ====== WRITE ACTIONS ======
if (action === "replace") {
  const [oldStr, newStrRaw] = args;
  if (!oldStr) { console.error("✗ 缺少 <old>"); process.exit(1); }
  const newStr = resolveArg(newStrRaw);
  if (!content.includes(oldStr)) {
    console.error("✗ 未找到: \"" + oldStr.slice(0, 60) + (oldStr.length > 60 ? "..." : "") + "\"");
    process.exit(1);
  }
  const count = (content.match(new RegExp(oldStr.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'g')) || []).length;
  const result = content.split(oldStr).join(newStr);
  write(file, result);
  console.error("  替换了 " + count + " 处");
  process.exit(0);
}

if (action === "replacere") {
  const [pattern, newStrRaw] = args;
  if (!pattern) { console.error("✗ 缺少 <pattern>"); process.exit(1); }
  const newStr = resolveArg(newStrRaw);
  const regex = new RegExp(pattern, 'g');
  if (!regex.test(content)) { regex.lastIndex = 0; console.error("✗ 未匹配正则: " + pattern); process.exit(1); }
  regex.lastIndex = 0;
  const count = (content.match(regex) || []).length;
  const result = content.replace(regex, newStr);
  write(file, result);
  console.error("  替换了 " + count + " 处");
  process.exit(0);
}

if (action === "insert-before" || action === "insert-after") {
  const [target, textRaw] = args;
  if (!target) { console.error("✗ 缺少 <target>"); process.exit(1); }
  const text = resolveArg(textRaw);
  if (text === undefined || text === null) { console.error("✗ 缺少 <text>"); process.exit(1); }
  const matched = lines.map((l,i)=>({line:l,i})).filter(o => o.line && o.line.includes(target));
  if (matched.length === 0) { console.error("✗ 未找到包含 \"" + target + "\" 的行"); process.exit(1); }
  const insertion = action === "insert-before" ? text + "\n" : text + "\n";
  const sorted = matched.sort((a, b) => action === "insert-before" ? b.i - a.i : a.i - b.i);
  let result = content;
  for (const m of sorted) {
    const pos = action === "insert-before"
      ? content.indexOf(m.line)
      : content.indexOf(m.line) + m.line.length + 1;
    result = result.slice(0, pos) + insertion + result.slice(pos);
    content = result;
  }
  write(file, result);
  console.error("  在 " + matched.length + " 处插入");
  process.exit(0);
}

if (action === "insert-line") {
  const [lineNoStr, textRaw] = args;
  if (!lineNoStr) { console.error("✗ 缺少 <lineno>"); process.exit(1); }
  const text = resolveArg(textRaw);
  const ln = parseInt(lineNoStr, 10);
  if (isNaN(ln) || ln < 0 || ln > lines.length) { console.error("✗ 行号无效: " + lineNoStr); process.exit(1); }
  const insertion = text + "\n";
  const pos = content.indexOf(lines[ln - 1]) + lines[ln - 1].length + 1;
  const result = content.slice(0, pos) + insertion + content.slice(pos);
  write(file, result);
  console.error("  在 " + ln + " 行后插入");
  process.exit(0);
}

if (action === "delete") {
  const [target] = args;
  if (!target) { console.error("✗ 缺少 <target>"); process.exit(1); }
  const toDelete = lines.map((l,i)=>({l,i})).filter(o => o.l && o.l.includes(target));
  if (toDelete.length === 0) { console.error("✗ 未找到包含 \"" + target + "\" 的行"); process.exit(1); }
  const deleteSet = new Set(toDelete.map(m => m.i));
  const result = lines.filter(l => !deleteSet.has(l.i)).map(l => l.line).join("\n");
  write(file, result);
  console.error("  删除了 " + deleteSet.size + " 行");
  process.exit(0);
}

if (action === "delete-range") {
  const [fromStr, toStr] = args;
  const from = parseInt(fromStr, 10);
  const to = parseInt(toStr, 10);
  if (isNaN(from) || isNaN(to)) { console.error("✗ 行号必须为数字"); process.exit(1); }
  const result = lines.filter(l => l.i + 1 < from || l.i + 1 > to).map(l => l.line).join("\n");
  write(file, result);
  console.error("  删除了第 " + from + "~" + to + " 行");
  process.exit(0);
}

if (action === "delete-line") {
  const [lineNoStr] = args;
  const ln = parseInt(lineNoStr, 10);
  if (isNaN(ln) || ln < 1 || ln > lines.length) { console.error("✗ 行号无效: " + lineNoStr); process.exit(1); }
  const result = lines.filter((_, i) => i + 1 !== ln).map(l => l.line).join("\n");
  write(file, result);
  console.error("  删除了第 " + ln + " 行");
  process.exit(0);
}

if (action === "append") {
  const [textRaw] = args;
  const text = resolveArg(textRaw);
  if (text === undefined || text === null) { console.error("✗ 缺少 <text>"); process.exit(1); }
  const suffix = content.endsWith("\n") ? "" : "\n";
  const result = content + suffix + text + "\n";
  write(file, result);
  console.error("  追加了 " + text.length + " 字符");
  process.exit(0);
}

console.error("✗ 未知 action: " + action);
process.exit(1);
