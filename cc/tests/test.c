// 機能ごとのテスト (chibicc の tests/ に倣った形)。
// assert_eq(期待値, 式, __LINE__) が失敗すると行番号を出して exit(1) する。
// ホストの cc でも同じファイルが通る (tests/host_shim.c が assert_eq を用意する)。
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

void assert_eq(long want, long got, int line);

int g_int = 42;
long g_long = -5000000000L;
char g_str[] = "abc";
char *g_ptr = "xyz";
int g_arr[5] = {1, 2, 3};
int *g_ap = &g_arr[2];
struct { char c; int x; } g_st = {'q', 77};
int g_zero;
static int s_counter;

enum color { RED, GREEN = 5, BLUE };
typedef int myint;
typedef struct pair { int a, b; } Pair;

int add2(int a, int b) { return a + b; }
int add8(int a, int b, int c, int d, int e, int f, int g, int h) { return a + b + c + d + e + f + g + h; }
int sub_(int a, int b) { return a - b; }
long fact(long n) { return n <= 1 ? 1 : n * fact(n - 1); }
void set(int *p, int v) { *p = v; }
char echo_char(char c) { return c; }
int counter() { return ++s_counter; }
int is_even(int n);
int is_odd(int n) { return n == 0 ? 0 : is_even(n - 1); }
int is_even(int n) { return n == 0 ? 1 : is_odd(n - 1); }
int apply(int (*f)(int, int), int a, int b) { return f(a, b); }

void test_arith() {
  assert_eq(0, 0, __LINE__);
  assert_eq(42, 42, __LINE__);
  assert_eq(21, 5 + 20 - 4, __LINE__);
  assert_eq(41, 12 + 34 - 5, __LINE__);
  assert_eq(47, 5 + 6 * 7, __LINE__);
  assert_eq(15, 5 * (9 - 6), __LINE__);
  assert_eq(4, (3 + 5) / 2, __LINE__);
  assert_eq(-3, -7 / 2, __LINE__);
  assert_eq(-1, -7 % 2, __LINE__);
  assert_eq(10, - -10, __LINE__);
  assert_eq(10, - - +10, __LINE__);
  assert_eq(3, 17 % 7, __LINE__);
  int a = 7, b = 3;
  assert_eq(2, a / b, __LINE__);
  assert_eq(1, a % b, __LINE__);
  assert_eq(-21, -a * b, __LINE__);
}

void test_compare() {
  assert_eq(0, 0 == 1, __LINE__);
  assert_eq(1, 42 == 42, __LINE__);
  assert_eq(1, 0 != 1, __LINE__);
  assert_eq(0, 42 != 42, __LINE__);
  assert_eq(1, 0 < 1, __LINE__);
  assert_eq(0, 1 < 1, __LINE__);
  assert_eq(1, 1 <= 1, __LINE__);
  assert_eq(0, 2 <= 1, __LINE__);
  assert_eq(1, 1 > 0, __LINE__);
  assert_eq(0, 1 > 1, __LINE__);
  assert_eq(1, 1 >= 1, __LINE__);
  assert_eq(0, 1 >= 2, __LINE__);
  int x = -1;
  assert_eq(1, x < 0, __LINE__);
  assert_eq(1, x < 1, __LINE__);
  long big = 5000000000L;
  assert_eq(1, big > 1, __LINE__);
}

void test_logical() {
  assert_eq(1, !0, __LINE__);
  assert_eq(0, !3, __LINE__);
  assert_eq(1, 1 && 2, __LINE__);
  assert_eq(0, 1 && 0, __LINE__);
  assert_eq(1, 0 || 5, __LINE__);
  assert_eq(0, 0 || 0, __LINE__);
  int n = 0;
  0 && (n = 1); // 短絡評価: 右辺は評価されない
  assert_eq(0, n, __LINE__);
  1 || (n = 2);
  assert_eq(0, n, __LINE__);
  1 && (n = 3);
  assert_eq(3, n, __LINE__);
  char *p = 0;
  assert_eq(0, p && *p, __LINE__); // NULL を参照しない
}

void test_bitwise() {
  assert_eq(1, 3 & 5, __LINE__);
  assert_eq(7, 3 | 5, __LINE__);
  assert_eq(6, 3 ^ 5, __LINE__);
  assert_eq(-1, ~0, __LINE__);
  assert_eq(-6, ~5, __LINE__);
  assert_eq(16, 1 << 4, __LINE__);
  assert_eq(3, 13 >> 2, __LINE__);
  assert_eq(-2, -8 >> 2, __LINE__); // 算術シフト
  assert_eq(1099511627776L, 1L << 40, __LINE__);
  int a = 0xf0;
  assert_eq(0xff, a | 0x0f, __LINE__);
  assert_eq(0x0f, (a >> 4) & 0xff, __LINE__);
}

void test_int_width() {
  int i = 2147483647;
  i = i + 1; // int は 32bit で回り込む
  assert_eq(-2147483648L, i, __LINE__);
  long l = 2147483647;
  l = l + 1;
  assert_eq(2147483648L, l, __LINE__);
  char c = 127;
  c++;
  assert_eq(-128, c, __LINE__);
  c = 300;
  assert_eq(44, c, __LINE__);
  short s = 40000;
  assert_eq(-25536, s, __LINE__);
  assert_eq(-1, (char)255, __LINE__);
  assert_eq(1, (char)257, __LINE__);
  assert_eq(-1, (int)4294967295L, __LINE__);
  assert_eq(4294967296L * 3, 4294967296L * 3, __LINE__);
  int m = 65536;
  assert_eq(0, m * m, __LINE__); // 32bit 乗算
  assert_eq(4294967296L, (long)m * m, __LINE__);
}

void test_assign() {
  int a = 3;
  assert_eq(3, a, __LINE__);
  int b = a = 5; // 代入は値を持つ
  assert_eq(5, b, __LINE__);
  a += 10;
  assert_eq(15, a, __LINE__);
  a -= 3;
  assert_eq(12, a, __LINE__);
  a *= 2;
  assert_eq(24, a, __LINE__);
  a /= 5;
  assert_eq(4, a, __LINE__);
  a %= 3;
  assert_eq(1, a, __LINE__);
  a <<= 4;
  assert_eq(16, a, __LINE__);
  a >>= 1;
  assert_eq(8, a, __LINE__);
  a |= 3;
  assert_eq(11, a, __LINE__);
  a &= 6;
  assert_eq(2, a, __LINE__);
  a ^= 7;
  assert_eq(5, a, __LINE__);
  int arr[3] = {1, 2, 3};
  arr[1] += 40;
  assert_eq(42, arr[1], __LINE__);
  int x, y, z;
  x = y = z = 9;
  assert_eq(27, x + y + z, __LINE__);
}

void test_incdec() {
  int i = 5;
  assert_eq(5, i++, __LINE__);
  assert_eq(6, i, __LINE__);
  assert_eq(7, ++i, __LINE__);
  assert_eq(7, i--, __LINE__);
  assert_eq(5, --i, __LINE__);
  int a[3] = {10, 20, 30};
  int *p = a;
  assert_eq(10, *p++, __LINE__);
  assert_eq(20, *p, __LINE__);
  assert_eq(30, *++p, __LINE__);
  assert_eq(31, ++*p, __LINE__);
  p--;
  assert_eq(20, *p, __LINE__);
  a[0]++;
  assert_eq(11, a[0], __LINE__);
}

void test_control() {
  int x = 0;
  if (1) x = 2;
  assert_eq(2, x, __LINE__);
  if (0) x = 3; else x = 4;
  assert_eq(4, x, __LINE__);
  if (x == 4) { if (x > 5) x = 1; else x = 7; }
  assert_eq(7, x, __LINE__);
  int sum = 0;
  for (int i = 0; i <= 10; i++) sum += i;
  assert_eq(55, sum, __LINE__);
  int i = 0;
  while (i < 10) i++;
  assert_eq(10, i, __LINE__);
  i = 0;
  do i += 3; while (i < 10);
  assert_eq(12, i, __LINE__);
  i = 100;
  do i++; while (0);
  assert_eq(101, i, __LINE__);
  sum = 0;
  for (i = 0; ; i++) {
    if (i == 10) break;
    if (i % 2) continue;
    sum += i;
  }
  assert_eq(20, sum, __LINE__);
  sum = 0;
  for (int a = 0; a < 3; a++)
    for (int b = 0; b < 3; b++) {
      if (b == 2) break;
      sum += 10 * a + b;
    }
  assert_eq(63, sum, __LINE__);
  int t = 1 ? 2 : 3;
  assert_eq(2, t, __LINE__);
  assert_eq(3, 0 ? 2 : 3, __LINE__);
  assert_eq(5, (x = 1, 5), __LINE__);
}

int classify(int n) {
  switch (n) {
  case 0:
    return 100;
  case 1:
  case 2:
    return 200;
  case GREEN:
    n = 50;
    break;
  default:
    return -1;
  }
  return n;
}

void test_switch() {
  assert_eq(100, classify(0), __LINE__);
  assert_eq(200, classify(1), __LINE__);
  assert_eq(200, classify(2), __LINE__);
  assert_eq(50, classify(5), __LINE__);
  assert_eq(-1, classify(9), __LINE__);
  int n = 0;
  for (int i = 0; i < 5; i++) {
    switch (i) {
    case 1:
      continue; // ループの continue
    case 3:
      n += 100; // フォールスルー
    case 4:
      n += 10;
      break;
    }
    n++;
  }
  assert_eq(124, n, __LINE__);
}

void test_scope() {
  int x = 1;
  {
    int x = 2;
    assert_eq(2, x, __LINE__);
    { int x = 3; assert_eq(3, x, __LINE__); }
  }
  assert_eq(1, x, __LINE__);
  for (int x = 10; x < 11; x++) assert_eq(10, x, __LINE__);
  assert_eq(1, x, __LINE__);
}

void test_pointer() {
  int x = 3;
  int *p = &x;
  assert_eq(3, *p, __LINE__);
  *p = 9;
  assert_eq(9, x, __LINE__);
  int **pp = &p;
  **pp = 11;
  assert_eq(11, x, __LINE__);
  set(&x, 20);
  assert_eq(20, x, __LINE__);
  int a[4] = {1, 2, 3, 4};
  assert_eq(3, *(a + 2), __LINE__);
  assert_eq(3, a[2], __LINE__);
  assert_eq(3, 2[a], __LINE__);
  int *q = a + 3;
  assert_eq(3, q - a, __LINE__);
  assert_eq(1, q > a, __LINE__);
  assert_eq(4, *q, __LINE__);
  assert_eq(2, q[-2], __LINE__);
  long la[3];
  assert_eq(16, (char *)&la[2] - (char *)&la[0], __LINE__);
  char *s = "hello";
  assert_eq('e', s[1], __LINE__);
  assert_eq('l', *(s + 3), __LINE__);
  void *v = a;
  assert_eq(2, ((int *)v)[1], __LINE__);
  assert_eq(0, (long)NULL, __LINE__);
}

void test_array() {
  int a[10];
  for (int i = 0; i < 10; i++) a[i] = i * i;
  assert_eq(81, a[9], __LINE__);
  assert_eq(40, sizeof(a), __LINE__);
  assert_eq(10, sizeof(a) / sizeof(a[0]), __LINE__);
  int m[3][4];
  for (int i = 0; i < 3; i++)
    for (int j = 0; j < 4; j++) m[i][j] = i * 10 + j;
  assert_eq(23, m[2][3], __LINE__);
  assert_eq(12, *(*(m + 1) + 2), __LINE__);
  assert_eq(48, sizeof(m), __LINE__);
  assert_eq(16, sizeof(m[0]), __LINE__);
  assert_eq(20, (int *)m[2] - (int *)m[0] + 12, __LINE__);
  int init[] = {5, 6, 7};
  assert_eq(12, sizeof(init), __LINE__);
  assert_eq(7, init[2], __LINE__);
  int part[5] = {1, 2};
  assert_eq(0, part[4], __LINE__);
  char s[] = "hi!";
  assert_eq(4, sizeof(s), __LINE__);
  assert_eq('!', s[2], __LINE__);
  assert_eq(0, s[3], __LINE__);
  int mm[2][3] = {{1, 2, 3}, {4, 5, 6}};
  assert_eq(6, mm[1][2], __LINE__);
}

struct S1 { char c; int i; };
struct S2 { char c; long l; char d; };
struct Node { int v; struct Node *next; };

void test_struct() {
  assert_eq(8, sizeof(struct S1), __LINE__);
  assert_eq(24, sizeof(struct S2), __LINE__);
  struct S1 s;
  s.c = 'a';
  s.i = 1000;
  assert_eq('a', s.c, __LINE__);
  assert_eq(1000, s.i, __LINE__);
  struct S1 *p = &s;
  p->i = 2000;
  assert_eq(2000, s.i, __LINE__);
  assert_eq(4, (char *)&s.i - (char *)&s, __LINE__);
  struct S1 arr[3];
  arr[2].i = 7;
  assert_eq(7, (arr + 2)->i, __LINE__);
  assert_eq(24, sizeof(arr), __LINE__);
  struct Node n2 = {2, 0};
  struct Node n1 = {1, &n2};
  assert_eq(2, n1.next->v, __LINE__);
  struct S1 t = s; // コピー
  s.i = 1;
  assert_eq(2000, t.i, __LINE__);
  t = s;
  assert_eq(1, t.i, __LINE__);
  Pair pr = {3, 4};
  assert_eq(7, pr.a + pr.b, __LINE__);
  struct { int x[3]; char y; } anon;
  anon.x[1] = 5;
  assert_eq(16, sizeof(anon), __LINE__);
  assert_eq(5, anon.x[1], __LINE__);
}

void test_global() {
  assert_eq(42, g_int, __LINE__);
  g_int++;
  assert_eq(43, g_int, __LINE__);
  assert_eq(-5000000000L, g_long, __LINE__);
  assert_eq(4, sizeof(g_str), __LINE__);
  assert_eq('c', g_str[2], __LINE__);
  assert_eq('y', g_ptr[1], __LINE__);
  assert_eq(3, g_arr[2], __LINE__);
  assert_eq(0, g_arr[4], __LINE__);
  assert_eq(3, *g_ap, __LINE__);
  assert_eq('q', g_st.c, __LINE__);
  assert_eq(77, g_st.x, __LINE__);
  assert_eq(0, g_zero, __LINE__);
}

void test_func() {
  assert_eq(7, add2(3, 4), __LINE__);
  assert_eq(36, add8(1, 2, 3, 4, 5, 6, 7, 8), __LINE__);
  assert_eq(-1, sub_(add2(1, 2), add2(2, 2)), __LINE__);
  assert_eq(3628800, fact(10), __LINE__);
  assert_eq(2432902008176640000L, fact(20), __LINE__);
  assert_eq('x', echo_char('x'), __LINE__);
  assert_eq(1, is_even(10), __LINE__);
  assert_eq(1, is_odd(7), __LINE__);
  assert_eq(1, counter(), __LINE__);
  assert_eq(2, counter(), __LINE__);
  int (*f)(int, int) = add2;
  assert_eq(30, f(10, 20), __LINE__);
  assert_eq(30, (*f)(10, 20), __LINE__);
  f = sub_;
  assert_eq(-10, f(10, 20), __LINE__);
  assert_eq(9, apply(add2, 4, 5), __LINE__);
  assert_eq(-1, apply(sub_, 4, 5), __LINE__);
  // 呼び出しの引数の中で別の呼び出し (レジスタが壊れないか)
  assert_eq(20, add8(add2(1, 1), 2, add2(1, 2), 4, 5, add2(add2(1, 1), 1), 1, 1) - 1, __LINE__);
}

void test_sizeof_enum_typedef() {
  assert_eq(1, sizeof(char), __LINE__);
  assert_eq(2, sizeof(short), __LINE__);
  assert_eq(4, sizeof(int), __LINE__);
  assert_eq(8, sizeof(long), __LINE__);
  assert_eq(8, sizeof(int *), __LINE__);
  assert_eq(8, sizeof(long long), __LINE__);
  int x;
  assert_eq(4, sizeof x, __LINE__);
  assert_eq(8, sizeof(x + 1L), __LINE__);
  assert_eq(0, RED, __LINE__);
  assert_eq(5, GREEN, __LINE__);
  assert_eq(6, BLUE, __LINE__);
  enum color c = BLUE;
  assert_eq(6, c, __LINE__);
  myint m = 3;
  assert_eq(4, sizeof(myint), __LINE__);
  assert_eq(3, m, __LINE__);
  typedef char buf8[8];
  buf8 b;
  assert_eq(8, sizeof(b), __LINE__);
}

void test_libc() {
  char buf[32];
  assert_eq(5, strlen("hello"), __LINE__);
  assert_eq(0, strcmp("abc", "abc"), __LINE__);
  assert_eq(1, strcmp("abc", "abd") < 0, __LINE__);
  strcpy(buf, "foo");
  strcat(buf, "bar");
  assert_eq(0, strcmp(buf, "foobar"), __LINE__);
  memset(buf, 'x', 3);
  assert_eq(0, strncmp(buf, "xxxbar", 6), __LINE__);
  memcpy(buf, "AB", 2);
  assert_eq('B', buf[1], __LINE__);
  assert_eq(-42, atoi("-42"), __LINE__);
  int *heap = malloc(100 * sizeof(int));
  for (int i = 0; i < 100; i++) heap[i] = i;
  assert_eq(99, heap[99], __LINE__);
  int *big = malloc(1000000);
  big[249999] = 5;
  assert_eq(5, big[249999], __LINE__);
  assert_eq(1, big != heap, __LINE__);
  char *z = calloc(10, 1);
  assert_eq(0, z[9], __LINE__);
}

// 式スタックが s1..s11 に収まらない深さ、2 KiB を超えるフレーム
void test_stress() {
  int r = 1 + (2 + (3 + (4 + (5 + (6 + (7 + (8 + (9 + (10 + (11 + (12 + (13 + add2(7, 7)))))))))))));
  assert_eq(105, r, __LINE__);
  char big[5000];
  int after = 3;
  big[0] = 1;
  big[4999] = 7;
  assert_eq(8, big[0] + big[4999], __LINE__);
  assert_eq(3, after, __LINE__);
  after += add8(1, 2, 3, 4, 5, 6, 7, big[4999]);
  assert_eq(38, after, __LINE__);
  long x = 1;
  for (int i = 0; i < 62; i++) x = x * 2;
  assert_eq(4611686018427387904L, x, __LINE__);
  assert_eq(-4611686018427387904L, -x, __LINE__);
}

int main() {
  test_stress();
  test_arith();
  test_compare();
  test_logical();
  test_bitwise();
  test_int_width();
  test_assign();
  test_incdec();
  test_control();
  test_switch();
  test_scope();
  test_pointer();
  test_array();
  test_struct();
  test_global();
  test_func();
  test_sizeof_enum_typedef();
  test_libc();
  printf("OK\n");
  return 0;
}
