// libc.h — rvcc がすべての .c ファイルの先頭に暗黙に読み込む宣言。
// (プリプロセッサが無いので、ソース中の #include <stdio.h> などは無視され、代わりにこれが使われる)

typedef long size_t;
enum { NULL = 0, EOF = -1 };

// crt0.s (システムコール)
void exit(int code);
long write(int fd, void *buf, long n);
long read(int fd, void *buf, long n);
long __brk(long addr);

// libc.c
int putchar(int c);
int puts(char *s);
int printf(char *fmt, ...);
void *malloc(long size);
void *calloc(long n, long size);
void free(void *p);
long strlen(char *s);
int strcmp(char *a, char *b);
int strncmp(char *a, char *b, long n);
char *strcpy(char *dst, char *src);
char *strcat(char *dst, char *src);
void *memcpy(void *dst, void *src, long n);
void *memset(void *dst, int c, long n);
int memcmp(void *a, void *b, long n);
int atoi(char *s);
int abs(int x);
void assert_eq(long want, long got, int line);
