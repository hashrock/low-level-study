// ホストの cc でテストを動かすときだけ使う assert_eq (rvcc では libc.c のものが使われる)
#include <stdio.h>
#include <stdlib.h>

void assert_eq(long want, long got, int line) {
  if (want == got) return;
  printf("line %d: expected %ld but got %ld\n", line, want, got);
  exit(1);
}
