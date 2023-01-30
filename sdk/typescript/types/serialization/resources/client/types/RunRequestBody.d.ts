/**
 * This file was auto-generated by Fern from our API Definition.
 */
import * as serializers from "../../..";
import { CakeworkApi } from "../../../..";
import * as core from "../../../../core";
export declare const RunRequestBody: core.serialization.ObjectSchema<serializers.RunRequestBody.Raw, CakeworkApi.RunRequestBody>;
export declare namespace RunRequestBody {
    interface Raw {
        parameters?: serializers.Parameters.Raw | null;
        compute?: serializers.Compute.Raw | null;
    }
}
