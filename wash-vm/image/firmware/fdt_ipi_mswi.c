/*
 * SPDX-License-Identifier: BSD-2-Clause
 *
 * Copyright (c) 2021 Western Digital Corporation or its affiliates.
 *
 * Authors:
 *   Anup Patel <anup.patel@wdc.com>
 */

#include <sbi/sbi_error.h>
#include <sbi/sbi_console.h>
#include <sbi/sbi_heap.h>
#include <sbi_utils/fdt/fdt_helper.h>
#include <sbi_utils/ipi/fdt_ipi.h>
#include <sbi_utils/ipi/aclint_mswi.h>

static int ipi_mswi_cold_init(void *fdt, int nodeoff,
			      const struct fdt_match *match)
{
	int rc;
	unsigned long offset;
	struct aclint_mswi_data *ms;

	sbi_printf("WASH/mswi: enter nodeoff=%d compat=%s\n", nodeoff,
		   match->compatible);

	ms = sbi_zalloc(sizeof(*ms));
	if (!ms)
		return SBI_ENOMEM;

	rc = fdt_parse_aclint_node(fdt, nodeoff, false, false,
				   &ms->addr, &ms->size, NULL, NULL,
				   &ms->first_hartid, &ms->hart_count);
	sbi_printf("WASH/mswi: parse rc=%d addr=0x%lx size=0x%lx fh=%u hc=%u\n",
		   rc, ms->addr, ms->size, ms->first_hartid, ms->hart_count);
	if (rc) {
		sbi_free(ms);
		return rc;
	}

	if (match->data) {
		offset = *((unsigned long *)match->data);
		sbi_printf("WASH/mswi: offset=0x%lx ACLINT_MSWI_SIZE=0x%x\n",
			   offset, ACLINT_MSWI_SIZE);
		ms->addr += offset;
		if ((ms->size - offset) < ACLINT_MSWI_SIZE) {
			sbi_printf("WASH/mswi: SIZE CHECK FAILED\n");
			return SBI_EINVAL;
		}
		ms->size = ACLINT_MSWI_SIZE;
	}

	rc = aclint_mswi_cold_init(ms);
	sbi_printf("WASH/mswi: aclint_mswi_cold_init rc=%d\n", rc);
	if (rc) {
		sbi_free(ms);
		return rc;
	}

	return 0;
}

static const unsigned long clint_offset = CLINT_MSWI_OFFSET;

static const struct fdt_match ipi_mswi_match[] = {
	{ .compatible = "riscv,clint0", .data = &clint_offset },
	{ .compatible = "sifive,clint0", .data = &clint_offset },
	{ .compatible = "thead,c900-clint", .data = &clint_offset },
	{ .compatible = "thead,c900-aclint-mswi" },
	{ .compatible = "riscv,aclint-mswi" },
	{ },
};

struct fdt_ipi fdt_ipi_mswi = {
	.match_table = ipi_mswi_match,
	.cold_init = ipi_mswi_cold_init,
	.warm_init = aclint_mswi_warm_init,
	.exit = NULL,
};
