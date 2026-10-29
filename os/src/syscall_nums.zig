//! システムコール番号 (カーネルとユーザで共有)。a7 に入れて ecall する。
pub const EXIT = 0;
pub const READ = 1;
pub const WRITE = 2;
pub const OPEN = 3;
pub const CLOSE = 4;
pub const MKDIR = 5;
pub const UNLINK = 6;
pub const CHDIR = 7;
pub const GETCWD = 8;
pub const READDIR = 9;
pub const PS = 10;
pub const UPTIME = 11;
pub const SLEEP = 12;
pub const SPAWN = 13;
pub const GETPID = 14;
pub const YIELD = 15;
pub const HALT = 16;

pub const O_CREAT = 1;
pub const O_TRUNC = 2;
pub const O_APPEND = 4;
