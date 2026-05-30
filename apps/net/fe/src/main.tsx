// wash-net FE entry. A6 ships the generated Advanced editor: ObjectForm bound to
// the codegen descriptor. Full app wiring (defineWashApp, BE validate/apply
// round-trip, the bespoke Tier-C screens) lands in Phase B against the
// wash-vm/vm harness. This entry mounts a standalone playground and re-exports
// the reusable pieces.

import { render } from "solid-js/web";
import { createSignal } from "solid-js";

import { ObjectForm } from "./ObjectForm.tsx";
import type { Descriptor } from "./objectform-model.ts";
import descriptorJson from "./generated/descriptor.json";

const desc = descriptorJson as unknown as Descriptor;

function Playground() {
  const [value, setValue] = createSignal<Record<string, any>>({
    Name: "lan",
    Device: "br-lan",
    Proto: { _tag: "static", IPAddr: "192.168.1.1/24" },
  });
  const iface = desc.objects.find((o) => o.kind === "network/interface")!;
  const onChange = (path: string, v: unknown) => {
    // Demo only: a real editor threads changes into a structured value/store
    // and POSTs the staged object to washnetd for validation (Phase B).
    console.log("change", path, v);
  };
  return (
    <ObjectForm
      object={iface}
      value={value()}
      pathPrefix="Interfaces[0]"
      showAdvanced
      onChange={onChange}
    />
  );
}

const root = document.getElementById("root");
if (root) render(() => <Playground />, root);

export { ObjectForm } from "./ObjectForm.tsx";
export * from "./objectform-model.ts";
