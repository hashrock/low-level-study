// libc.c — 最小の C ライブラリ。rvcc 自身でコンパイルされる (dogfooding)。
// システムコールの write/read/exit/__brk だけ crt0.s にある。

// ---- 文字列・メモリ ----

long strlen(char *s) {
  char *p = s;
  while (*p) p++;
  return p - s;
}

int strcmp(char *a, char *b) {
  while (*a && *a == *b) {
    a++;
    b++;
  }
  return *a - *b;
}

int strncmp(char *a, char *b, long n) {
  for (; n > 0; n--, a++, b++)
    if (*a != *b || !*a) return *a - *b;
  return 0;
}

char *strcpy(char *dst, char *src) {
  char *d = dst;
  while ((*d++ = *src++)) ;
  return dst;
}

char *strcat(char *dst, char *src) {
  strcpy(dst + strlen(dst), src);
  return dst;
}

void *memcpy(void *dst, void *src, long n) {
  char *d = dst;
  char *s = src;
  while (n-- > 0) *d++ = *s++;
  return dst;
}

void *memset(void *dst, int c, long n) {
  char *d = dst;
  while (n-- > 0) *d++ = c;
  return dst;
}

int memcmp(void *a, void *b, long n) {
  char *p = a;
  char *q = b;
  for (; n > 0; n--, p++, q++)
    if (*p != *q) return *p - *q;
  return 0;
}

int atoi(char *s) {
  int sign = 1, n = 0;
  if (*s == '-') {
    sign = -1;
    s++;
  }
  while (*s >= '0' && *s <= '9') n = n * 10 + (*s++ - '0');
  return sign * n;
}

int abs(int x) { return x < 0 ? -x : x; }

// ---- メモリ割り当て: brk を伸ばすだけのバンプアロケータ (free は何もしない) ----

static char *heap_cur;
static char *heap_end;

void *malloc(long size) {
  size = (size + 15) & ~15;
  if (!heap_cur) {
    heap_cur = (char *)((__brk(0) + 15) & ~15);
    heap_end = heap_cur;
  }
  if (heap_cur + size > heap_end) {
    long grow = size > 65536 ? size : 65536;
    char *want = heap_end + grow;
    if ((char *)__brk((long)want) != want) return NULL;
    heap_end = want;
  }
  char *p = heap_cur;
  heap_cur += size;
  return p;
}

void *calloc(long n, long size) { return memset(malloc(n * size), 0, n * size); }

void free(void *p) {}

// ---- 出力 ----

int putchar(int c) {
  char b = c;
  write(1, &b, 1);
  return c;
}

int puts(char *s) {
  write(1, s, strlen(s));
  write(1, "\n", 1);
  return 0;
}

// printf は 1 回の呼び出しの出力をここにためて、最後に write 1 回で出す。
static char obuf[256];
static int olen;
static int ocount;

static void out(char c) {
  ocount++;
  if (olen == sizeof(obuf)) {
    write(1, obuf, olen);
    olen = 0;
  }
  obuf[olen++] = c;
}

// 数値 v を base 進で出す。負の数は桁を負のまま取り出す (LONG_MIN も正しく出せる)。
static void outnum(long v, int base, int width, char pad, int left) {
  char buf[24];
  int n = 0, neg = v < 0;
  do {
    int d = v % base;
    if (d < 0) d = -d;
    buf[n++] = "0123456789abcdef"[d];
    v /= base;
  } while (v);
  if (neg && pad == '0') { // -0005: 符号は 0 埋めの前
    out('-');
    width--;
  } else if (neg) {
    buf[n++] = '-';
  }
  int len = n;
  if (!left)
    for (; len < width; len++) out(pad);
  while (n > 0) out(buf[--n]);
  if (left)
    for (; len < width; len++) out(' ');
}

// 対応: %d %i %u %ld %x %lx %p %c %s %%、幅 (%5d)、0 埋め (%08x)、左寄せ (%-5s)。
// 可変長引数: RISC-V では可変長引数も a0..a7 で渡るので、関数の入口で a0..a7 を
// 並べて保存し (rvcc の可変長関数の prologue)、__va_start() でその「次の引数」を指す。
int printf(char *fmt, ...) {
  long *ap = __va_start();
  ocount = 0;
  for (; *fmt; fmt++) {
    if (*fmt != '%') {
      out(*fmt);
      continue;
    }
    fmt++;
    int left = 0, width = 0, islong = 0;
    char pad = ' ';
    if (*fmt == '-') {
      left = 1;
      fmt++;
    }
    if (*fmt == '0') pad = '0';
    while (*fmt >= '0' && *fmt <= '9') width = width * 10 + (*fmt++ - '0');
    while (*fmt == 'l') {
      islong = 1;
      fmt++;
    }
    long v;
    switch (*fmt) {
    case 'd':
    case 'i':
    case 'u':
      v = *ap++;
      outnum(islong ? v : (int)v, 10, width, pad, left);
      break;
    case 'x':
    case 'p':
      v = *ap++;
      if (*fmt == 'p') {
        out('0');
        out('x');
      } else if (!islong) {
        v = v & 0xffffffffL;
      }
      if (v < 0) { // 上位ビットが立った 64bit 値: 上下 2 回に分けて出す
        outnum((v >> 32) & 0xffffffffL, 16, width - 8, pad, 0);
        outnum(v & 0xffffffffL, 16, 8, '0', 0);
      } else {
        outnum(v, 16, width, pad, left);
      }
      break;
    case 'c':
      out(*ap++);
      break;
    case 's': {
      char *s = (char *)*ap++;
      int n = strlen(s);
      for (int i = n; i < width && !left; i++) out(' ');
      while (*s) out(*s++);
      for (int i = n; i < width && left; i++) out(' ');
      break;
    }
    case '%':
      out('%');
      break;
    }
  }
  write(1, obuf, olen);
  olen = 0;
  return ocount;
}

// テスト用: want と got が違えば行番号を出して終了する (tests/test.c が使う)。
void assert_eq(long want, long got, int line) {
  if (want == got) return;
  printf("line %d: expected %ld but got %ld\n", line, want, got);
  exit(1);
}
