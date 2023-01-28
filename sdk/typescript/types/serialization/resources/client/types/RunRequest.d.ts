/**
 * This file was auto-generated by Fern from our API Definition.
 */
import * as serializers from "../../..";
import { CakeworkApi } from "../../../..";
import * as core from "../../../../core";
export declare const RunRequest: core.serialization.ObjectSchema<serializers.RunRequest.Raw, CakeworkApi.RunRequest>;
export declare namespace RunRequest {
    interface Raw {
        task: serializers.Task.Raw;
        parameters?: serializers.Parameters.Raw | null;
        compute?: serializers.Compute.Raw | null;
    }
}
