// エラトステネスの篩: 配列とループ
#include <stdio.h>

char composite[10001]; // グローバル配列は .bss に置かれ 0 で始まる

int main() {
  int n = 10000, count = 0;
  for (int i = 2; i * i <= n; i++)
    if (!composite[i])
      for (int j = i * i; j <= n; j += i) composite[j] = 1;
  printf("primes up to 100:");
  for (int i = 2; i <= n; i++) {
    if (composite[i]) continue;
    count++;
    if (i <= 100) printf(" %d", i);
  }
  printf("\ncount up to %d: %d\n", n, count);
  int last = n;
  while (composite[last]) last--;
  printf("largest: %d\n", last);
  return 0;
}
