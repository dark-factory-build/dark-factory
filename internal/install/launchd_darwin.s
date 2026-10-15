#include "textflag.h"

TEXT libc_launch_activate_socket_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_launch_activate_socket(SB)
GLOBL	·libc_launch_activate_socket_trampoline_addr(SB), RODATA, $8
DATA	·libc_launch_activate_socket_trampoline_addr(SB)/8, $libc_launch_activate_socket_trampoline<>(SB)

TEXT libc_free_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_free(SB)
GLOBL	·libc_free_trampoline_addr(SB), RODATA, $8
DATA	·libc_free_trampoline_addr(SB)/8, $libc_free_trampoline<>(SB)
