// クイックソート (比較関数は関数ポインタで渡す)
#include <stdio.h>


int seed = 12345;
int rand_next() { // 線形合同法 (int のオーバーフローを避けて long で計算)
  seed = (int)((seed * 1103515245L + 12345) % 2147483648L);
  return seed >> 16;
}

void swap(int *a, int *b) {
  int t = *a;
  *a = *b;
  *b = t;
}

void quicksort(int *a, int lo, int hi, int (*cmp)(int, int)) {
  if (lo >= hi) return;
  int pivot = a[(lo + hi) / 2], i = lo, j = hi;
  while (i <= j) {
    while (cmp(a[i], pivot) < 0) i++;
    while (cmp(a[j], pivot) > 0) j--;
    if (i <= j) swap(&a[i++], &a[j--]);
  }
  quicksort(a, lo, j, cmp);
  quicksort(a, i, hi, cmp);
}

int ascending(int x, int y) { return x - y; }
int descending(int x, int y) { return y - x; }

int is_sorted(int *a, int n, int (*cmp)(int, int)) {
  for (int i = 1; i < n; i++)
    if (cmp(a[i - 1], a[i]) > 0) return 0;
  return 1;
}

int data[1000];

int main() {
  for (int i = 0; i < 1000; i++) data[i] = rand_next() % 10000;
  printf("before: %d %d %d %d %d ...\n", data[0], data[1], data[2], data[3], data[4]);
  quicksort(data, 0, 999, ascending);
  printf("sorted: %d %d %d %d %d ... %d\n", data[0], data[1], data[2], data[3], data[4], data[999]);
  printf("is_sorted(asc) = %d\n", is_sorted(data, 1000, ascending));
  quicksort(data, 0, 999, descending);
  printf("desc:   %d %d %d ... %d\n", data[0], data[1], data[2], data[999]);
  printf("is_sorted(desc) = %d\n", is_sorted(data, 1000, descending));
  long sum = 0;
  for (int *p = data; p < data + 1000; p++) sum += *p;
  printf("sum = %ld\n", sum);
  return 0;
}
