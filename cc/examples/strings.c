// 文字列関数とポインタ演算
#include <stdio.h>
#include <string.h>

int my_strlen(char *s) {
  char *p = s;
  while (*p) p++;
  return p - s; // ポインタの差 = 要素数
}

void reverse(char *s) {
  char *l = s, *r = s + strlen(s) - 1;
  while (l < r) {
    char t = *l;
    *l++ = *r;
    *r-- = t;
  }
}

int count_char(char *s, char c) {
  int n = 0;
  for (; *s; s++) n += *s == c;
  return n;
}

void to_upper(char *s) {
  for (; *s; s++)
    if (*s >= 'a' && *s <= 'z') *s -= 'a' - 'A';
}

int main() {
  char buf[64];
  char *greeting = "hello, world";
  printf("len(\"%s\") = %d / %d\n", greeting, my_strlen(greeting), (int)strlen(greeting));
  strcpy(buf, greeting);
  reverse(buf);
  printf("reversed: %s\n", buf);
  to_upper(buf);
  printf("upper: %s\n", buf);
  printf("count 'l': %d\n", count_char(greeting, 'l'));
  strcpy(buf, "foo");
  strcat(buf, "bar");
  strcat(buf, "baz");
  printf("concat: %s (%d)\n", buf, (int)strlen(buf));
  printf("cmp: %d %d %d\n", strcmp("abc", "abc") == 0, strcmp("abc", "abd") < 0, strcmp("b", "a") > 0);
  // ポインタ演算は要素の大きさで拡大される
  int nums[5] = {10, 20, 30, 40, 50};
  int *p = nums;
  long *q = (long *)0;
  printf("p[3] = %d, *(p + 1) = %d, 2[p] = %d\n", p[3], *(p + 1), 2[p]);
  printf("&nums[4] - &nums[1] = %d\n", (int)(&nums[4] - &nums[1]));
  printf("byte distance: %d, long step: %d\n", (int)((char *)&nums[4] - (char *)nums), (int)(long)(q + 3));
  char *words[] = {"alpha", "beta", "gamma"};
  for (int i = 0; i < 3; i++) printf("%s%c", words[i], i < 2 ? ' ' : '\n');
  printf("%5d|%-5d|%05d|%x|%c%c\n", 42, 42, 42, 255, 'o', 'k');
  return 0;
}
