// fetch.js — network access, gated by --allow-net.
// run: go run . examples/fetch.js --allow-net=example.com
// (no top-level await: scripts are classic scripts, not ES modules)
async function main() {
  const url = Sandbox.args[0] ?? "https://example.com/";
  try {
    const r = await fetch(url);
    const body = await r.text();
    console.log("status:", r.status, "ok:", r.ok, "bytes:", body.length);
    console.log("title:", (body.match(/<title>(.*?)<\/title>/) || [])[1]);
  } catch (e) {
    console.log("fetch failed:", e.name + ":", e.message);
  }
}
main();
