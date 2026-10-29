// async_fs.js — async file ops resolved back onto the event loop.
// run: go run . examples/async_fs.js --allow-read=examples/data --allow-write=examples/data
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  const text = await Sandbox.readTextFile("examples/data/input.txt");
  console.log("read:", JSON.stringify(text));

  // Two reads + a timer in flight at the same time: the loop keeps running
  // while Go goroutines do the I/O.
  const started = Date.now();
  const [a, b] = await Promise.all([
    Sandbox.readTextFile("examples/data/input.txt"),
    Sandbox.readTextFile("examples/data/input.txt"),
    sleep(20),
  ]);
  console.log("parallel reads equal:", a === b, "elapsed>=20ms:", Date.now() - started >= 20);

  const out = "examples/data/output.txt";
  await Sandbox.writeTextFile(out, `written at step 3, length=${text.length}\n`);
  console.log("wrote:", JSON.stringify(await Sandbox.readTextFile(out)));

  try {
    await Sandbox.readTextFile("examples/data/missing.txt");
  } catch (e) {
    console.log("missing file ->", e.name);
  }

  // require() of a local module is also gated by --allow-read.
  const lib = require("./data/lib.js");
  console.log("require:", lib.greet("minirt"));

  console.log("permissions.query read examples/data:", Sandbox.permissions.query("read", "examples/data"));
  console.log("permissions.query read /etc/passwd:", Sandbox.permissions.query("read", "/etc/passwd"));
}

main();
console.log("main() returned synchronously; I/O is still pending");
