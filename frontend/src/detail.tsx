import { Nullable } from "tsdef";

import Dialog, { DialogProps } from "@mui/material/Dialog";
import DialogContent from "@mui/material/DialogContent";
import DialogTitle from "@mui/material/DialogTitle";

import { Grid } from "@mui/material";
import moment from "moment";

import { useState, useCallback } from "react";

import "./app.less";
import { cli } from "./api";
import { formatFilesize } from "./tools";

import "./detail.less";
import { FileGetReply, Tape } from "./entity";

export type Detail = FileGetReply & {
  tapes: Map<bigint, Tape>;
};

export const useDetailModal = () => {
  const [detail, setDetail] = useState<Nullable<Detail>>(null);
  const openDetailModel = useCallback(
    (detail: FileGetReply) => {
      (async () => {
        const tapeList = await cli.tapeMGet({
          ids: detail.positions.map((posi) => posi.tapeId),
        }).response;

        const tapes = new Map<bigint, Tape>();
        for (const tape of tapeList.tapes) {
          tapes.set(tape.id, tape);
        }

        setDetail({ ...detail, tapes });
      })();
    },
    [setDetail]
  );
  const closeDetailModel = useCallback(() => {
    setDetail(null);
  }, [setDetail]);

  return { detail, closeDetailModel, openDetailModel };
};

export const DetailModal = (props: Omit<DialogProps, "open" | "children"> & { detail: Nullable<Detail> }) => {
  const { detail, ...otherProps } = props;
  if (!detail) {
    return null;
  }

  return (
    <Dialog className="detail-content" open={!!detail} scroll="body" {...otherProps}>
      <DialogTitle id="scroll-dialog-title">{detail.file?.name}</DialogTitle>

      <div className="position">
        {detail.positions.map((posi) => {
          const tape = detail.tapes?.get(posi.tapeId);
          if (!tape) {
            return null;
          }

          return (
            <DialogContent dividers={true} key={`${posi.id}`}>
              <Grid container spacing={1}>
                <Grid item xs={4}>
                  <p>
                    <b>Tape ID</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{tape?.barcode}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Tape Name</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{tape?.name}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Tape Create Time</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{tape?.createTime ? moment.unix(Number(tape.createTime)).format() : "--"}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Tape Destroy Time</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{tape?.destroyTime ? (moment(Number(tape.destroyTime)).format() as string) : "--"}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Tape Capacity</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{formatFilesize(tape?.capacityBytes)}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Tape Writen</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{formatFilesize(tape?.writenBytes)}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Path</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <pre>{posi.path}</pre>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Permission</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{(Number(posi.mode) & 0o777).toString(8)}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Modify Time</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{moment.unix(Number(posi.modTime)).format()}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Write Time</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{moment.unix(Number(posi.writeTime)).format()}</p>
                </Grid>

                <Grid item xs={4}>
                  <p>
                    <b>Size</b>
                  </p>
                </Grid>
                <Grid item xs={8}>
                  <p>{formatFilesize(posi.size)}</p>
                </Grid>
              </Grid>
            </DialogContent>
          );
        })}
      </div>
      {/* <DialogContentText
          id="scroll-dialog-description"
          ref={descriptionElementRef}
          tabIndex={-1}
        >
        </DialogContentText> */}
      {/* <DialogActions>
        <Button onClick={handleClose}>Cancel</Button>
        <Button onClick={handleClose}>Subscribe</Button>
      </DialogActions> */}
    </Dialog>
  );

  // return <Modal open={!!detail} {...otherProps}></Modal>;
};
