// struct と malloc で作る連結リスト
#include <stdio.h>
#include <stdlib.h>

struct node {
  int value;
  struct node *next;
};

typedef struct node Node;

Node *push(Node *head, int v) {
  Node *n = malloc(sizeof(Node));
  n->value = v;
  n->next = head;
  return n;
}

Node *reverse(Node *head) {
  Node *prev = NULL;
  while (head) {
    Node *next = head->next;
    head->next = prev;
    prev = head;
    head = next;
  }
  return prev;
}

void print(Node *n) {
  printf("[");
  for (; n; n = n->next) printf(n->next ? "%d, " : "%d", n->value);
  printf("]\n");
}

int length(Node *n) { return n ? 1 + length(n->next) : 0; }

// 条件に合う要素を取り除く (ポインタへのポインタで先頭も同じ扱い)
void remove_if_even(Node **p) {
  while (*p) {
    if ((*p)->value % 2 == 0) *p = (*p)->next;
    else p = &(*p)->next;
  }
}

int main() {
  Node *list = NULL;
  for (int i = 1; i <= 10; i++) list = push(list, i * i);
  print(list);
  list = reverse(list);
  print(list);
  printf("length = %d\n", length(list));
  remove_if_even(&list);
  print(list);
  long sum = 0;
  for (Node *n = list; n; n = n->next) sum += n->value;
  printf("sum of odd squares = %ld\n", sum);
  return 0;
}
