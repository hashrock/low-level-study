// hello.js — the smallest program. No permissions needed.
console.log("Hello from minirt!");
console.log("args:", Sandbox.args);
console.log({ engine: "goja", nested: { list: [1, 2, 3], ok: true } });
console.error("this line goes to stderr");
