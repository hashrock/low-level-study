// struct のレイアウト: サイズ・オフセット・詰め物 (padding)
#include <stdio.h>

struct A { char c; int i; char d; };        // 1 + 3(pad) + 4 + 1 + 3(pad) = 12
struct B { char c; long l; };               // 1 + 7(pad) + 8 = 16
struct C { char a; char b; short s; int i; }; // 詰め物なしで 8
struct D { struct A a; char tail[3]; };     // 12 + 3 + 1(pad) = 16
struct E { int *p; char name[5]; struct B b; };


// offsetof の昔ながらの書き方: アドレス 0 にある struct のメンバのアドレス
long off_A_i() { return (long)&((struct A *)0)->i; }
long off_A_d() { return (long)&((struct A *)0)->d; }
long off_B_l() { return (long)&((struct B *)0)->l; }
long off_D_tail() { return (long)&((struct D *)0)->tail; }
long off_E_name() { return (long)&((struct E *)0)->name; }
long off_E_b() { return (long)&((struct E *)0)->b; }

struct point { int x, y; };
struct rect { struct point min, max; };

int area(struct rect *r) { return (r->max.x - r->min.x) * (r->max.y - r->min.y); }

int main() {
  printf("sizeof: char=%d short=%d int=%d long=%d ptr=%d\n", (int)sizeof(char), (int)sizeof(short),
         (int)sizeof(int), (int)sizeof(long), (int)sizeof(char *));
  printf("struct A: size %d, i@%d d@%d\n", (int)sizeof(struct A), (int)off_A_i(), (int)off_A_d());
  printf("struct B: size %d, l@%d\n", (int)sizeof(struct B), (int)off_B_l());
  printf("struct C: size %d\n", (int)sizeof(struct C));
  printf("struct D: size %d, tail@%d\n", (int)sizeof(struct D), (int)off_D_tail());
  printf("struct E: size %d, name@%d b@%d\n", (int)sizeof(struct E), (int)off_E_name(), (int)off_E_b());
  struct A arr[4];
  printf("struct A[4]: size %d, &arr[3] - &arr[0] = %d bytes\n", (int)sizeof(arr),
         (int)((char *)&arr[3] - (char *)&arr[0]));

  struct rect r = {{1, 2}, {11, 7}};
  printf("area = %d\n", area(&r));
  struct rect r2 = r; // struct のコピー
  r2.max.x = 21;
  printf("area = %d, original = %d\n", area(&r2), area(&r));
  struct point *pts = &r.min;
  pts[1].y = 12; // r.max.y
  printf("r.max.y = %d\n", r.max.y);
  return 0;
}
