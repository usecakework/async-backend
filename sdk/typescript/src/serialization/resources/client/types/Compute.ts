/**
 * This file was auto-generated by Fern from our API Definition.
 */

import * as serializers from "../../..";
import { CakeworkApi } from "../../../..";
import * as core from "../../../../core";

export const Compute: core.serialization.ObjectSchema<
  serializers.Compute.Raw,
  CakeworkApi.Compute
> = core.serialization.object({
  cpu: core.serialization.number().optional(),
  memory: core.serialization.number().optional(),
});

export declare namespace Compute {
  interface Raw {
    cpu?: number | null;
    memory?: number | null;
  }
}
