// sandbox_escape.js — every attempt here must be blocked.
// run: go run . examples/sandbox_escape.js --allow-read=examples/data --timeout=2s
const results = [];
async function attempt(label, fn) {
  try {
    const v = await fn();
    results.push(`ESCAPED  ${label}: ${String(v).slice(0, 40)}`);
  } catch (e) {
    results.push(`blocked  ${label}: ${e.name}: ${e.message}`);
  }
}

async function main() {
  await attempt("read /etc/passwd", () => Sandbox.readTextFile("/etc/passwd"));
  await attempt("../ escape", () => Sandbox.readTextFile("examples/data/../../go.mod"));
  await attempt("deep ../ escape", () => Sandbox.readTextFile("examples/data/../../../../../../etc/passwd"));
  await attempt("symlink escape", () => Sandbox.readTextFile("examples/data/link_to_etc/passwd"));
  await attempt("require outside", () => require("../main.go"));
  await attempt("write (no --allow-write)", () => Sandbox.writeTextFile("examples/data/pwned.txt", "x"));
  await attempt("fetch without --allow-net", () => fetch("https://example.com/"));
  await attempt("file:// fetch", () => fetch("file:///etc/passwd"));
  await attempt("env without --allow-env", () => Sandbox.env.get("HOME"));
  await attempt("node-style process", () => process.env.HOME);
  await attempt("Function constructor reaching globals", () => Function("return typeof require('fs')")());

  for (const r of results) console.log(r);

  console.log("now spinning forever; --timeout must stop this...");
  for (;;) {}
}
main();
