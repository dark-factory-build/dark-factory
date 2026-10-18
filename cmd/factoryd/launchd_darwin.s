#include "textflag.h"

TEXT libc_launch_activate_socket_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_launch_activate_socket(SB)
GLOBL	·libc_launch_activate_socket_trampoline_addr(SB), RODATA, $8
DATA	·libc_launch_activate_socket_trampoline_addr(SB)/8, $libc_launch_activate_socket_trampoline<>(SB)
