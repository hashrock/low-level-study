// 再帰版フィボナッチ (../compiler/examples/fib.lang と同じ内容)
#include <stdio.h>

int fib(int n) {
  if (n < 2) return n;
  return fib(n - 1) + fib(n - 2);
}

// ループ版。fib(50) は 32bit に収まらないので long
long fib_loop(int n) {
  long a = 0, b = 1;
  while (n > 0) {
    long t = a + b;
    a = b;
    b = t;
    n -= 1;
  }
  return a;
}

int main() {
  for (int i = 0; i < 15; i++) printf("fib(%d) = %d\n", i, fib(i));
  printf("fibLoop(50) = %ld\n", fib_loop(50));
  printf("fib(25) = %d\n", fib(25));
  return 0;
}
