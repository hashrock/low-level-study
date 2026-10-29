// args: hello "RISC-V world" 42
// コマンドライン引数: _start がスタックから argc / argv を取り出して main に渡す
#include <stdio.h>
#include <stdlib.h>

int main(int argc, char **argv) {
  printf("argc = %d\n", argc);
  for (int i = 1; i < argc; i++) printf("argv[%d] = \"%s\"\n", i, argv[i]);
  int sum = 0;
  for (char **p = argv + 1; *p; p++) sum += atoi(*p);
  printf("sum of numeric args = %d\n", sum);
  return argc - 1;
}
