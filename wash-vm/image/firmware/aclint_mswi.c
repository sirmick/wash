/*
 * SPDX-License-Identifier: BSD-2-Clause
 *
 * Copyright (c) 2021 Western Digital Corporation or its affiliates.
 *
 * Authors:
 *   Anup Patel <anup.patel@wdc.com>
 */

#include <sbi/riscv_asm.h>
#include <sbi/riscv_atomic.h>
#include <sbi/riscv_io.h>
#include <sbi/sbi_console.h>
#include <sbi/sbi_domain.h>
#include <sbi/sbi_error.h>
#include <sbi/sbi_ipi.h>
#include <sbi/sbi_scratch.h>
#include <sbi/sbi_timer.h>
#include <sbi_utils/ipi/aclint_mswi.h>

static unsigned long mswi_ptr_offset;

#define mswi_set_hart_data_ptr(__scratch, __mswi)	\
	sbi_scratch_write_type((__scratch), void *, mswi_ptr_offset, (__mswi))

#define mswi_get_hart_data_ptr(__scratch)		\
	sbi_scratch_read_type((__scratch), void *, mswi_ptr_offset)

static void mswi_ipi_send(u32 hart_index)
{
	u32 *msip;
	struct sbi_scratch *scratch;
	struct aclint_mswi_data *mswi;

	scratch = sbi_hartindex_to_scratch(hart_index);
	if (!scratch)
		return;

	mswi = mswi_get_hart_data_ptr(scratch);
	if (!mswi)
		return;

	/* Set ACLINT IPI */
	msip = (void *)mswi->addr;
	writel_relaxed(1, &msip[sbi_hartindex_to_hartid(hart_index) -
				mswi->first_hartid]);
}

static void mswi_ipi_clear(u32 hart_index)
{
	u32 *msip;
	struct sbi_scratch *scratch;
	struct aclint_mswi_data *mswi;

	scratch = sbi_hartindex_to_scratch(hart_index);
	if (!scratch)
		return;

	mswi = mswi_get_hart_data_ptr(scratch);
	if (!mswi)
		return;

	/* Clear ACLINT IPI */
	msip = (void *)mswi->addr;
	writel_relaxed(0, &msip[sbi_hartindex_to_hartid(hart_index) -
				mswi->first_hartid]);
}

static struct sbi_ipi_device aclint_mswi = {
	.name = "aclint-mswi",
	.ipi_send = mswi_ipi_send,
	.ipi_clear = mswi_ipi_clear
};

int aclint_mswi_warm_init(void)
{
	/* Clear IPI for current HART */
	mswi_ipi_clear(current_hartid());

	return 0;
}

int aclint_mswi_cold_init(struct aclint_mswi_data *mswi)
{
	u32 i;
	int rc;
	struct sbi_scratch *scratch;
	unsigned long pos, region_size;
	struct sbi_domain_memregion reg;

	sbi_printf("WASH/aclint: enter addr=0x%lx size=0x%lx fh=%u hc=%u\n",
		   mswi ? mswi->addr : 0, mswi ? mswi->size : 0,
		   mswi ? mswi->first_hartid : 0, mswi ? mswi->hart_count : 0);

	/* Sanity checks */
	if (!mswi) {
		sbi_printf("WASH/aclint: SANITY FAIL: mswi NULL\n");
		return SBI_EINVAL;
	}
	if (mswi->addr & (ACLINT_MSWI_ALIGN - 1)) {
		sbi_printf("WASH/aclint: SANITY FAIL: addr not aligned\n");
		return SBI_EINVAL;
	}
	if (mswi->size < (mswi->hart_count * sizeof(u32))) {
		sbi_printf("WASH/aclint: SANITY FAIL: size too small\n");
		return SBI_EINVAL;
	}
	if (!mswi->hart_count) {
		sbi_printf("WASH/aclint: SANITY FAIL: hart_count == 0\n");
		return SBI_EINVAL;
	}
	if (mswi->hart_count > ACLINT_MSWI_MAX_HARTS) {
		sbi_printf("WASH/aclint: SANITY FAIL: hart_count too big\n");
		return SBI_EINVAL;
	}

	if (!mswi_ptr_offset) {
		mswi_ptr_offset = sbi_scratch_alloc_type_offset(void *);
		if (!mswi_ptr_offset) {
			sbi_printf("WASH/aclint: scratch_alloc failed\n");
			return SBI_ENOMEM;
		}
	}

	for (i = 0; i < mswi->hart_count; i++) {
		scratch = sbi_hartid_to_scratch(mswi->first_hartid + i);
		if (!scratch)
			continue;
		mswi_set_hart_data_ptr(scratch, mswi);
	}

	for (pos = 0; pos < mswi->size; pos += ACLINT_MSWI_ALIGN) {
		region_size = ((mswi->size - pos) < ACLINT_MSWI_ALIGN) ?
			      (mswi->size - pos) : ACLINT_MSWI_ALIGN;
		sbi_domain_memregion_init(mswi->addr + pos, region_size,
					  (SBI_DOMAIN_MEMREGION_MMIO |
					   SBI_DOMAIN_MEMREGION_M_READABLE |
					   SBI_DOMAIN_MEMREGION_M_WRITABLE),
					  &reg);
		rc = sbi_domain_root_add_memregion(&reg);
		if (rc) {
			sbi_printf("WASH/aclint: add_memregion failed rc=%d\n", rc);
			return rc;
		}
	}

	sbi_ipi_set_device(&aclint_mswi);
	sbi_printf("WASH/aclint: done\n");

	return 0;
}
