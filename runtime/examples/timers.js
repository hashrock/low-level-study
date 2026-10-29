// timers.js — ordering of sync code, microtasks and macrotasks (timers).
// Expected order: 1 sync, 2 sync end, 3 micro(promise), 4 micro(queueMicrotask),
// 5 micro(await), 6 timeout 0, 7 micro inside timeout, 8 timeout 0 (second),
// 9 timeout 10, interval x3, done
const log = (s) => console.log(s);

setTimeout(() => {
  log("6 timeout 0");
  Promise.resolve().then(() => log("7 micro inside timeout (runs before next timer)"));
}, 0);
setTimeout(() => log("8 timeout 0 (second)"), 0);
setTimeout(() => log("9 timeout 10"), 10);

Promise.resolve().then(() => log("3 micro (promise.then)"));
queueMicrotask(() => log("4 micro (queueMicrotask)"));
(async () => {
  await null;
  log("5 micro (after await)");
})();

const cancelled = setTimeout(() => log("never printed"), 5);
clearTimeout(cancelled);

let n = 0;
const iv = setInterval(() => {
  n++;
  log(`interval tick ${n}`);
  if (n === 3) {
    clearInterval(iv);
    log("done");
  }
}, 20);

log("1 sync");
log("2 sync end");
