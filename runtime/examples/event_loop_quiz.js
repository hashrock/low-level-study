// event_loop_quiz.js — predict the order of the lines before running!
// run: go run . examples/event_loop_quiz.js
// Rules: sync code first; then *all* microtasks (then / await / queueMicrotask)
// in FIFO order; only then the next macrotask (a timer). Each timer callback
// is its own task, followed by its own microtasks.
console.log("script start");

setTimeout(() => {
  console.log("timeout 0");
  Promise.resolve().then(() => console.log("then inside timeout 0"));
}, 0);

setTimeout(() => console.log("timeout 5"), 5);

async function worker() {
  console.log("worker: before first await");
  await null;
  console.log("worker: after await null");
  await new Promise((resolve) => setTimeout(resolve, 0));
  console.log("worker: after awaiting a timer");
}

Promise.resolve()
  .then(() => console.log("then #1"))
  .then(() => console.log("then #2 (chained)"));
worker();
queueMicrotask(() => console.log("queueMicrotask"));

console.log("script end");
